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
	"strings"
	"time"

	"github.com/ammario/tlru"
	"github.com/beatlabs/github-auth/app"
	"github.com/coder/labeler/ghapi"
	"github.com/coder/labeler/httpjson"
	"github.com/coder/retry"
	"github.com/go-chi/chi/v5"
	githook "github.com/go-playground/webhooks/v6/github"
	"github.com/google/go-github/v59/github"
	"github.com/sashabaranov/go-openai"
)

type repoAddr struct {
	install, user, repo string
}

type Service struct {
	Log       *slog.Logger
	OpenAI    *openai.Client
	AppConfig *app.Config
	Model     string

	router *chi.Mux

	// These caches are primarily useful in the test system, where there are
	// many inference requests to the same repo in a short period of time.
	//
	// That will rarely happen in production.
	repoLabelsCache *tlru.Cache[repoAddr, []*github.Label]

	recentIssuesCache *tlru.Cache[repoAddr, []*github.Issue]
}

func (s *Service) Init() {
	s.router = chi.NewRouter()
	s.router.Mount("/infer", httpjson.Handler(s.infer))
	s.router.Mount("/webhook", httpjson.Handler(s.webhook))

	s.repoLabelsCache = tlru.New[repoAddr](func(ls []*github.Label) int {
		return len(ls)
	}, 4096)
	s.recentIssuesCache = tlru.New[repoAddr](func(ls []*github.Issue) int {
		return len(ls)
	}, 4096)
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

type InferRequest struct {
	InstallID string `json:"install_id"`
	User      string `json:"user"`
	Repo      string `json:"repo"`
	Issue     int    `json:"issue"`
}

type InferResponse struct {
	SetLabels  []string `json:"set_labels,omitempty"`
	TokensUsed int      `json:"tokens_used,omitempty"`
}

func (s *Service) Infer(ctx context.Context, req *InferRequest) (*InferResponse, error) {
	instConfig, err := s.AppConfig.InstallationConfig(req.InstallID)
	if err != nil {
		return nil, fmt.Errorf("get installation config: %w", err)
	}

	githubClient := github.NewClient(instConfig.Client(ctx))

	lastIssues, err := s.recentIssuesCache.Do(repoAddr{
		install: req.InstallID,
		user:    req.User,
		repo:    req.Repo,
	}, func() ([]*github.Issue, error) {
		return ghapi.Page(
			ctx,
			githubClient,
			func(ctx context.Context, opt *github.ListOptions) ([]*github.Issue, *github.Response, error) {
				issues, resp, err := githubClient.Issues.ListByRepo(
					ctx,
					req.User,
					req.Repo,
					&github.IssueListByRepoOptions{
						State:       "all",
						ListOptions: *opt,
					},
				)

				return ghapi.OnlyTrueIssues(issues), resp, err
			},
			100,
		)
	}, time.Minute)
	if err != nil {
		return nil, fmt.Errorf("list issues: %w", err)
	}

	labels, err := s.repoLabelsCache.Do(repoAddr{
		install: req.InstallID,
		user:    req.User,
		repo:    req.Repo,
	}, func() ([]*github.Label, error) {
		return ghapi.Page(
			ctx,
			githubClient,
			func(ctx context.Context, opt *github.ListOptions) ([]*github.Label, *github.Response, error) {
				return githubClient.Issues.ListLabels(ctx, req.User, req.Repo, opt)
			},
			// We use the coder/customers label count as a reasonable maximum.
			300,
		)
	}, time.Minute)
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

retryAI:
	ret := retry.New(time.Second, time.Second*10)
	resp, err := s.OpenAI.CreateChatCompletion(
		ctx,
		aiContext.Request(s.Model),
	)
	if err != nil {
		var aiErr *openai.APIError
		if errors.As(err, &aiErr) {
			if (aiErr.HTTPStatusCode >= 500 || aiErr.HTTPStatusCode == 429) && ret.Wait(ctx) {
				s.Log.Warn("retrying AI call", "error", err)
				goto retryAI
			}
		}
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
		var setLabelsStr struct {
			Labels string `json:"labels"`
		}

		// Sometimes the labels are returned as a string.
		err2 := json.Unmarshal([]byte(toolCall.Function.Arguments), &setLabelsStr)
		if err2 != nil {
			return nil, errors.Join(
				fmt.Errorf("unmarshal setLabels: %w, toolCall: %+v", err, toolCall),
				err2,
			)
		}
	}

	disabledLabels := make(map[string]struct{})
	for _, label := range labels {
		if strings.Contains(label.GetDescription(), magicDisableString) {
			disabledLabels[label.GetName()] = struct{}{}
		}
	}

	// Remove any labels that are disabled.
	var newLabels []string
	for _, label := range setLabels.Labels {
		if _, ok := disabledLabels[label]; ok {
			continue
		}
		newLabels = append(newLabels, label)
	}

	return &InferResponse{
		SetLabels:  newLabels,
		TokensUsed: resp.Usage.TotalTokens,
	}, nil
}

func (s *Service) infer(w http.ResponseWriter, r *http.Request) *httpjson.Response {
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

	resp, err := s.Infer(r.Context(), &InferRequest{
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

func (s *Service) serverError(msg error) *httpjson.Response {
	s.Log.Error("server error", "error", msg)
	return &httpjson.Response{
		Status: http.StatusInternalServerError,
		Body:   httpjson.M{"error": msg.Error()},
	}
}

func (s *Service) webhook(w http.ResponseWriter, r *http.Request) *httpjson.Response {
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

	if payload.Action != "opened" && payload.Action != "reopened" {
		return &httpjson.Response{
			Status: http.StatusOK,
			Body:   httpjson.M{"message": "not an opened issue"},
		}
	}

	repo := payload.Repository

	resp, err := s.Infer(r.Context(), &InferRequest{
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
		Body:   httpjson.M{"message": "labels set", "labels": resp.SetLabels},
	}
}

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}
