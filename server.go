package labeler

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"

	"github.com/beatlabs/github-auth/app"
	"github.com/coder/labeler/httpjson"
	"github.com/go-chi/chi/v5"
	"github.com/google/go-github/v59/github"
	"github.com/sashabaranov/go-openai"
)

type Server struct {
	Log       *slog.Logger
	OpenAI    *openai.Client
	AppConfig *app.Config

	router *chi.Mux
}

func (s *Server) Init() {
	s.router = chi.NewRouter()
	s.router.Mount("/infer", httpjson.Handler(s.infer))
}

func filterSlice(slice []*github.Issue, f func(*github.Issue) bool) []*github.Issue {
	var result []*github.Issue
	for _, item := range slice {
		if f(item) {
			result = append(result, item)
		}
	}
	return result
}

type inferResponse struct {
	SetLabels  []string `json:"set_labels,omitempty"`
	TokensUsed int      `json:"tokens_used,omitempty"`
}

func (s *Server) infer(w http.ResponseWriter, r *http.Request) *httpjson.Response {
	var (
		installID = r.URL.Query().Get("install_id")
		user      = r.URL.Query().Get("user")
		repo      = r.URL.Query().Get("repo")
		issue     = r.URL.Query().Get("issue")
	)

	if user == "" || repo == "" || issue == "" || installID == "" {
		return &httpjson.Response{
			Status: http.StatusBadRequest,
			Body:   httpjson.M{"error": "install_id, user, repo, and issue are required"},
		}
	}

	instConfig, err := s.AppConfig.InstallationConfig(installID)
	if err != nil {
		return httpjson.ErrorMessage(
			http.StatusInternalServerError,
			fmt.Errorf("get installation config: %w", err),
		)
	}

	githubClient := github.NewClient(instConfig.Client(r.Context()))

	lastIssues, _, err := githubClient.Issues.ListByRepo(
		r.Context(),
		user,
		repo,
		&github.IssueListByRepoOptions{
			State: "all",
			ListOptions: github.ListOptions{
				PerPage: 100,
			},
		},
	)
	if err != nil {
		return &httpjson.Response{
			Status: http.StatusInternalServerError,
			Body:   httpjson.M{"error": err.Error()},
		}
	}

	labels, _, err := githubClient.Issues.ListLabels(r.Context(), user, repo, &github.ListOptions{
		PerPage: 100,
	})
	if err != nil {
		return &httpjson.Response{
			Status: http.StatusInternalServerError,
			Body:   httpjson.M{"error": err.Error()},
		}
	}

	issueNum, err := strconv.Atoi(issue)
	if err != nil {
		return &httpjson.Response{
			Status: http.StatusBadRequest,
			Body:   httpjson.M{"error": "invalid issue number"},
		}
	}

	targetIssue, _, err := githubClient.Issues.Get(r.Context(), user, repo, issueNum)
	if err != nil {
		return httpjson.ErrorMessage(
			http.StatusInternalServerError,
			fmt.Errorf("get target issue: %w", err),
		)
	}

	// Take out target issue from the list of issues
	lastIssues = filterSlice(lastIssues, func(i *github.Issue) bool {
		return i.GetNumber() != targetIssue.GetNumber()
	})

	// Sort by created at.
	sort.Slice(lastIssues, func(i, j int) bool {
		iTime := lastIssues[i].GetCreatedAt().Time
		jTime := lastIssues[j].GetCreatedAt().Time
		return iTime.Before(jTime)
	})

	aiContext := &context{
		allLabels:   labels,
		lastIssues:  lastIssues,
		targetIssue: targetIssue,
	}

	resp, err := s.OpenAI.CreateChatCompletion(
		r.Context(),
		aiContext.Request(),
	)
	if err != nil {
		return httpjson.ErrorMessage(
			http.StatusInternalServerError,
			fmt.Errorf("create chat completion: %w", err),
		)
	}

	if len(resp.Choices) != 1 {
		return &httpjson.Response{
			Status: http.StatusInternalServerError,
			Body:   httpjson.M{"error": "expected one choice"},
		}
	}

	choice := resp.Choices[0]

	if len(choice.Message.ToolCalls) != 1 {
		return &httpjson.Response{
			Status: http.StatusInternalServerError,
			Body:   httpjson.M{"error": "expected one tool call"},
		}
	}

	toolCall := choice.Message.ToolCalls[0]
	var setLabels struct {
		Labels []string `json:"labels"`
	}
	err = json.Unmarshal([]byte(toolCall.Function.Arguments), &setLabels)
	if err != nil {
		return httpjson.ErrorMessage(
			http.StatusInternalServerError,
			fmt.Errorf("unmarshal setLabels: %w", err),
		)
	}

	return &httpjson.Response{
		Status: http.StatusOK,
		Body: inferResponse{
			SetLabels:  setLabels.Labels,
			TokensUsed: resp.Usage.TotalTokens,
		},
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}
