package labeler

import (
	"encoding/json"
	"strings"

	"github.com/ammario/prefixsuffix"
	"github.com/coder/labeler/httpjson"
	"github.com/google/go-github/v59/github"
	"github.com/google/uuid"
	"github.com/sashabaranov/go-openai"
	"github.com/sashabaranov/go-openai/jsonschema"
)

// context contains and generates the GPT-4 context used for label generation.
type context struct {
	allLabels   []*github.Label
	lastIssues  []*github.Issue
	targetIssue *github.Issue
}

func (c *context) labelNames() []string {
	var labels []string
	for _, label := range c.allLabels {
		labels = append(labels, label.GetName())
	}
	return labels
}

func issueToText(issue *github.Issue) string {
	var sb strings.Builder
	sb.WriteString("title: " + issue.GetTitle())
	sb.WriteString("\n")

	saver := prefixsuffix.Saver{
		// Max 1000 characters per issue.
		N: 1000,
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

// Request generates the messages to be used in the GPT-4 context.
func (c *context) Request() openai.ChatCompletionRequest {
	var labelsDescription strings.Builder
	for _, label := range c.allLabels {
		labelsDescription.WriteString(label.GetName())
		labelsDescription.WriteString(": ")
		labelsDescription.WriteString(label.GetDescription())
		labelsDescription.WriteString("\n")
	}

	const labelFuncName = "setLabels"
	request := openai.ChatCompletionRequest{
		Model: openai.GPT4TurboPreview,
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
								Type:        jsonschema.Array,
								Items:       &jsonschema.Definition{Type: jsonschema.String},
								Enum:        c.labelNames(),
								Description: "The labels to apply to the issue.\n" + labelsDescription.String(),
							},
						},
					},
				},
			},
		},
	}
	var msgs []openai.ChatCompletionMessage

	msgs = append(msgs, openai.ChatCompletionMessage{
		Role: "system",
		Content: `You are a bot that helps labels issues on GitHub using the "setLabel"
		function.`,
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

	// Finally, add target issue.
	msgs = append(msgs, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: issueToText(c.targetIssue),
	})

	request.Messages = msgs
	return request
}
