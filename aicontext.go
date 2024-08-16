package labeler

import (
	"fmt"
	"strings"

	"github.com/ammario/prefixsuffix"
	"github.com/google/go-github/v59/github"
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
	fmt.Fprintf(&sb, "=== ISSUE %v ===\n", issue.GetNumber())
	fmt.Fprintf(&sb, "author: %s (%s)\n", issue.GetUser().GetLogin(), issue.GetAuthorAssociation())
	var labels []string
	for _, label := range issue.Labels {
		labels = append(labels, label.GetName())
	}
	fmt.Fprintf(&sb, "labels: %s\n", labels)
	sb.WriteString("title: " + issue.GetTitle())
	sb.WriteString("\n")

	saver := prefixsuffix.Saver{
		// Max 1000 characters per issue.
		N: 500,
	}
	saver.Write([]byte(issue.GetBody()))
	sb.Write(saver.Bytes())
	fmt.Fprintf(&sb, "\n=== END ISSUE %v ===\n", issue.GetNumber())

	return sb.String()
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

constructMsgs:
	var msgs []openai.ChatCompletionMessage

	// System message with instructions
	msgs = append(msgs, openai.ChatCompletionMessage{
		Role: "system",
		Content: `You are a bot that helps label issues on GitHub using the "setLabels"
		function. Do not apply labels that are meant for Pull Requests. Avoid applying labels when
		the label description says something like "` + magicDisableString + `".
		Only apply labels when absolutely certain they are correct. An accidental
		omission of a label is better than an accidental addition.
		Multiple labels can be applied to a single issue if appropriate.`,
	})

	// System message with label descriptions
	msgs = append(msgs,
		openai.ChatCompletionMessage{
			Role:    "system",
			Content: "The labels available are: \n" + labelsDescription.String(),
		},
	)

	// Create a single blob of past issues
	var pastIssuesBlob strings.Builder
	pastIssuesBlob.WriteString("Here are some examples of past issues and their labels:\n\n")

	for _, issue := range c.lastIssues {
		pastIssuesBlob.WriteString(issueToText(issue))
		pastIssuesBlob.WriteString("\n\n")
	}

	// Add past issues blob as a system message
	msgs = append(msgs, openai.ChatCompletionMessage{
		Role:    "system",
		Content: pastIssuesBlob.String(),
	})

	// Add the target issue
	msgs = append(msgs, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: issueToText(c.targetIssue),
	})

	modelTokenLimit := 128000

	// Check token limit and adjust if necessary
	if countTokens(msgs...) > modelTokenLimit && len(c.lastIssues) > 1 {
		// Reduce the number of past issues
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
