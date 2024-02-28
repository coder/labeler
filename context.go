package labeler

import (
	"strings"

	"github.com/google/go-github/v59/github"
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

// Request generates the messages to be used in the GPT-4 context.
func (c *context) Request() openai.ChatCompletionRequest {
	var labelsDescription strings.Builder
	for _, label := range c.allLabels {
		labelsDescription.WriteString(label.GetName())
		labelsDescription.WriteString(": ")
		labelsDescription.WriteString(label.GetDescription())
		labelsDescription.WriteString("\n")
	}

	request := openai.ChatCompletionRequest{
		Model: openai.GPT4TurboPreview,
		Tools: []openai.Tool{
			{
				Type: openai.ToolTypeFunction,
				Function: &openai.FunctionDefinition{
					Name:        "label",
					Description: `Label the GitHub issue with the given labels.`,
					Parameters: jsonschema.Definition{
						Description: "The labels to apply to the issue.\n" + labelsDescription.String(),
						Type:        jsonschema.Array,
						Items:       &jsonschema.Definition{Type: jsonschema.String},
						Enum:        c.labelNames(),
					},
				},
			},
		},
	}
	var msgs []openai.ChatCompletionMessage

	msgs = append(msgs, openai.ChatCompletionMessage{
		Role: "system",
		Content: `You are a bot that helps labels issues on GitHub using the "label"
		function. Pass zero or more labels to the "label" function to label the
		issue.`,
	})

	request.Messages = msgs
	return request
}
