package labeler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"

	"github.com/beatlabs/github-auth/app"
	"github.com/coder/labeler/httpjson"
	"github.com/go-chi/chi/v5"
	githook "github.com/go-playground/webhooks/v6/github"
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
	s.router.Mount("/webhook", httpjson.Handler(s.webhook))
}

func filterIssues(slice []*github.Issue, f func(*github.Issue) bool) []*github.Issue {
	var result []*github.Issue
	for _, item := range slice {
		if f(item) {
			result = append(result, item)
		}
	}
	return result
}

type inferRequest struct {
	InstallID string `json:"install_id"`
	User      string `json:"user"`
	Repo      string `json:"repo"`
	Issue     int    `json:"issue"`
}
type inferResponse struct {
	SetLabels  []string `json:"set_labels,omitempty"`
	TokensUsed int      `json:"tokens_used,omitempty"`
}

func (s *Server) runInfer(ctx context.Context, req *inferRequest) (*inferResponse, error) {
	instConfig, err := s.AppConfig.InstallationConfig(req.InstallID)
	if err != nil {
		return nil, fmt.Errorf("get installation config: %w", err)
	}

	githubClient := github.NewClient(instConfig.Client(ctx))

	lastIssues, _, err := githubClient.Issues.ListByRepo(
		ctx,
		req.User,
		req.Repo,
		&github.IssueListByRepoOptions{
			State: "all",
			ListOptions: github.ListOptions{
				PerPage: 100,
			},
		},
	)
	if err != nil {
		return nil, fmt.Errorf("list issues: %w", err)
	}

	labels, _, err := githubClient.Issues.ListLabels(ctx, req.User, req.Repo, &github.ListOptions{
		PerPage: 100,
	})
	if err != nil {
		return nil, fmt.Errorf("list labels: %w", err)
	}

	targetIssue, _, err := githubClient.Issues.Get(ctx, req.User, req.Repo, req.Issue)
	if err != nil {
		return nil, fmt.Errorf("get target issue: %w", err)
	}

	// Take out target issue from the list of issues
	lastIssues = filterIssues(lastIssues, func(i *github.Issue) bool {
		return i.GetNumber() != targetIssue.GetNumber()
	})

	// Sort by created at.
	sort.Slice(lastIssues, func(i, j int) bool {
		iTime := lastIssues[i].GetCreatedAt().Time
		jTime := lastIssues[j].GetCreatedAt().Time
		return iTime.Before(jTime)
	})

	aiContext := &aiContext{
		allLabels:   labels,
		lastIssues:  lastIssues,
		targetIssue: targetIssue,
	}

	resp, err := s.OpenAI.CreateChatCompletion(
		ctx,
		aiContext.Request(),
	)
	if err != nil {
		return nil, fmt.Errorf("create chat completion: %w", err)
	}

	if len(resp.Choices) != 1 {
		return nil, fmt.Errorf("expected one choice")
	}

	choice := resp.Choices[0]

	if len(choice.Message.ToolCalls) != 1 {
		return nil, fmt.Errorf("expected one tool call")
	}

	toolCall := choice.Message.ToolCalls[0]
	var setLabels struct {
		Labels []string `json:"labels"`
	}
	err = json.Unmarshal([]byte(toolCall.Function.Arguments), &setLabels)
	if err != nil {
		return nil, fmt.Errorf("unmarshal setLabels: %w", err)
	}

	return &inferResponse{
		SetLabels:  setLabels.Labels,
		TokensUsed: resp.Usage.TotalTokens,
	}, nil
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

	issueNum, err := strconv.Atoi(issue)
	if err != nil {
		return &httpjson.Response{
			Status: http.StatusBadRequest,
			Body:   httpjson.M{"error": "issue must be a number"},
		}
	}

	resp, err := s.runInfer(r.Context(), &inferRequest{
		InstallID: installID,
		User:      user,
		Repo:      repo,
		Issue:     issueNum,
	})
	if err != nil {
		return httpjson.ErrorMessage(http.StatusInternalServerError, err)
	}

	return &httpjson.Response{
		Status: http.StatusOK,
		Body:   resp,
	}
}

func (s *Server) serverError(msg error) *httpjson.Response {
	s.Log.Error("server error", "error", msg)
	return &httpjson.Response{
		Status: http.StatusInternalServerError,
		Body:   httpjson.M{"error": msg.Error()},
	}
}

func (s *Server) webhook(w http.ResponseWriter, r *http.Request) *httpjson.Response {
	hook, err := githook.New()
	if err != nil {
		if errors.Is(err, githook.ErrEventNotSpecifiedToParse) {
			return &httpjson.Response{
				Status: http.StatusOK,
				Body:   httpjson.M{"msg": "ignoring event: not specified to parse"},
			}
		}
		return s.serverError(err)
	}

	payloadAny, err := hook.Parse(
		r, githook.IssuesEvent,
	)
	if err != nil {
		return s.serverError(err)
	}

	payload, ok := payloadAny.(githook.IssuesPayload)
	if !ok {
		return s.serverError(fmt.Errorf("expected issues payload: %T", payloadAny))
	}

	if payload.Action != "opened" {
		return &httpjson.Response{
			Status: http.StatusOK,
			Body:   httpjson.M{"message": "not an opened issue"},
		}
	}

	repo := payload.Repository

	resp, err := s.runInfer(r.Context(), &inferRequest{
		InstallID: strconv.FormatInt(payload.Installation.ID, 10),
		User:      repo.Owner.Login,
		Repo:      repo.Name,
		Issue:     int(payload.Issue.Number),
	})
	if err != nil {
		return s.serverError(err)
	}

	// Set the labels.
	instConfig, err := s.AppConfig.InstallationConfig(strconv.FormatInt(payload.Installation.ID, 10))
	if err != nil {
		return s.serverError(err)
	}

	githubClient := github.NewClient(instConfig.Client(r.Context()))
	_, _, err = githubClient.Issues.AddLabelsToIssue(
		r.Context(),
		repo.Owner.Login,
		repo.Name,
		int(payload.Issue.Number),
		resp.SetLabels,
	)
	if err != nil {
		return s.serverError(err)
	}

	return &httpjson.Response{
		Status: http.StatusOK,
		Body:   httpjson.M{"message": "labels set"},
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}
