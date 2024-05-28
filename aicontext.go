package labeler

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"

	"github.com/ammario/prefixsuffix"
	"github.com/coder/labeler/httpjson"
	"github.com/google/go-github/v59/github"
	"github.com/google/uuid"
	"github.com/sashabaranov/go-openai"
	"github.com/sashabaranov/go-openai/jsonschema"
	"github.com/tiktoken-go/tokenizer"
)

// aiContext contains and generates the GPT-4 aiContext used for label generation.
type aiContext struct {
	allLabels   []*github.Label
	lastIssues  []*github.Issue
	targetIssue *github.Issue
}

func (c *aiContext) labelNames() []string {
	var labels []string
	for _, label := range c.allLabels {
		labels = append(labels, label.GetName())
	}
	return labels
}

func issueToText(issue *github.Issue) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "author: %s (%s)\n", issue.GetUser().GetLogin(), issue.GetAuthorAssociation())
	sb.WriteString("title: " + issue.GetTitle())
	sb.WriteString("\n")

	saver := prefixsuffix.Saver{
		// Max 1000 characters per issue.
		N: 500,
	}
	saver.Write([]byte(issue.GetBody()))
	sb.Write(saver.Bytes())

	return sb.String()
}

func mustJSON(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func countTokens(msgs ...openai.ChatCompletionMessage) int {
	enc, err := tokenizer.Get(tokenizer.Cl100kBase)
	if err != nil {
		panic("oh oh")
	}

	var tokens int
	for _, msg := range msgs {
		ts, _, _ := enc.Encode(msg.Content)
		tokens += len(ts)

		for _, call := range msg.ToolCalls {
			ts, _, _ = enc.Encode(call.Function.Arguments)
			tokens += len(ts)
		}
	}
	return tokens
}

// magicDisableString is deprecated as the original recommendation
// for disabling inscriptive labels.
const magicDisableString = "Only humans may set this"

// Request generates the messages to be used in the GPT-4 context.
func (c *aiContext) Request(
	model string,
) openai.ChatCompletionRequest {
	var labelsDescription strings.Builder
	for _, label := range c.allLabels {
		labelsDescription.WriteString(label.GetName())
		labelsDescription.WriteString(": ")
		labelsDescription.WriteString(label.GetDescription())
		labelsDescription.WriteString("\n")
	}

	const labelFuncName = "setLabels"
	request := openai.ChatCompletionRequest{
		Model: model,
		// We use LogProbs to determine level of confidence.
		LogProbs: true,
		// Want high determinism.
		Temperature: 0,
		Tools: []openai.Tool{
			{
				Type: openai.ToolTypeFunction,
				Function: &openai.FunctionDefinition{
					Name:        labelFuncName,
					Description: `Label the GitHub issue with the given labels.`,
					Parameters: jsonschema.Definition{
						Type: jsonschema.Object,
						Properties: map[string]jsonschema.Definition{
							"labels": {
								Type:  jsonschema.Array,
								Items: &jsonschema.Definition{Type: jsonschema.String},
								Enum:  c.labelNames(),
							},
						},
					},
				},
			},
		},
	}

	// See https://gist.github.com/ammario/6321e803f78f21e3ae87ab4f9e26a4e7
	// for slight performance improvement.
	rand.Shuffle(len(c.lastIssues), func(i, j int) {
		c.lastIssues[i], c.lastIssues[j] = c.lastIssues[j], c.lastIssues[i]
	})

constructMsgs:
	var msgs []openai.ChatCompletionMessage

	msgs = append(msgs, openai.ChatCompletionMessage{
		Role: "system",
		Content: `You are a bot that helps labels issues on GitHub using the "setLabel"
		function. Do not apply labels that are meant for Pull Requests. Avoid applying labels when
		the label description says something like "` + magicDisableString + `". The labels available are:
		` + labelsDescription.String(),
	})

	for _, issue := range c.lastIssues {
		msgs = append(msgs, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleUser,
			Content: issueToText(issue),
		})

		var labelNames []string
		for _, label := range issue.Labels {
			labelNames = append(labelNames, label.GetName())
		}

		tcID := uuid.NewString()
		msgs = append(msgs, openai.ChatCompletionMessage{
			Role: openai.ChatMessageRoleAssistant,
			ToolCalls: []openai.ToolCall{
				{
					Type: openai.ToolTypeFunction,
					ID:   tcID,
					Function: openai.FunctionCall{
						Name: labelFuncName,
						Arguments: mustJSON(httpjson.M{
							"labels": labelNames,
						}),
					},
				},
			},
		})

		msgs = append(msgs, openai.ChatCompletionMessage{
			Role:       openai.ChatMessageRoleTool,
			Content:    "OK",
			ToolCallID: tcID,
		})
	}

	targetIssueMsg := openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: issueToText(c.targetIssue),
	}

	// Finally, add target issue.
	msgs = append(msgs, targetIssueMsg)

	var modelTokenLimit int

	switch model {
	case openai.GPT3Dot5Turbo, openai.GPT3Dot5Turbo16K:
		modelTokenLimit = 16385
	case openai.GPT4TurboPreview:
		modelTokenLimit = 128000
	default:
		// Assume a big context window, errors are better than
		// bad performance.
		modelTokenLimit = 128000
	}

	// Prune messages if we are over the token limit.
	if countTokens(msgs...) > modelTokenLimit && len(c.lastIssues) > 1 {
		c.lastIssues = c.lastIssues[:len(c.lastIssues)/2]
		goto constructMsgs
	}

	request.Messages = msgs
	return request
}

func tokenize(text string) []string {
	enc, err := tokenizer.Get(tokenizer.Cl100kBase)
	if err != nil {
		panic(err)
	}

	_, strs, err := enc.Encode(text)
	if err != nil {
		panic(err)
	}

	return strs
}
