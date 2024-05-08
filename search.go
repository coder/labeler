package labeler

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"cloud.google.com/go/bigquery"
	"github.com/ammario/tlru"
	"github.com/beatlabs/github-auth/app"
	"github.com/coder/labeler/ghapi"
	"github.com/coder/labeler/httpjson"
	"github.com/go-chi/chi/v5"
	"github.com/google/go-github/v59/github"
	"github.com/sashabaranov/go-openai"
	"google.golang.org/api/iterator"
)

type Search struct {
	Log             *slog.Logger
	OpenAI          *openai.Client
	AppConfig       *app.Config
	BigQuery        *bigquery.Client
	repoToInstallID *tlru.Cache[string, int64]
}

func (s *Search) Init(r *chi.Mux) {
	s.repoToInstallID = tlru.New[string, int64](tlru.ConstantCost, 4096)
	r.Mount("/search", httpjson.Handler(s.search))
}

func (s *Search) getCachedRepoIssues(ctx context.Context,
	owner string,
	repo string,
) ([]*BqIssue, error) {
	query := s.BigQuery.Query(`
		SELECT * FROM ` + "ghindex.`" + issuesTableName + "`" + ` WHERE repo = @repo
		AND user = @owner AND state = 'open'
	`)

	query.Parameters = []bigquery.QueryParameter{
		{
			Name:  "repo",
			Value: repo,
		},
		{
			Name:  "owner",
			Value: owner,
		},
	}

	it, err := query.Read(ctx)
	if err != nil {
		return nil, err
	}

	var issues []*BqIssue
	for {
		var issue BqIssue
		err := it.Next(&issue)
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		issues = append(issues, &issue)
	}

	return issues, nil
}

func (s *Search) search(w http.ResponseWriter, r *http.Request) *httpjson.Response {
	owner := r.URL.Query().Get("owner")
	if owner == "" {
		return &httpjson.Response{
			Status: http.StatusBadRequest,
			Body:   httpjson.M{"error": "missing owner"},
		}
	}

	repoName := r.URL.Query().Get("repo")
	if repoName == "" {
		return &httpjson.Response{
			Status: http.StatusBadRequest,
			Body:   httpjson.M{"error": "missing repo"},
		}
	}

	httpClient := s.AppConfig.Client()

	installID, err := s.repoToInstallID.Do(owner+"/"+repoName, func() (int64, error) {
		return ghapi.InstallIDForRepo(r.Context(), httpClient, owner, repoName)
	}, time.Minute)
	if err != nil {
		return &httpjson.Response{
			Status: http.StatusInternalServerError,
			Body:   httpjson.M{"error": err.Error()},
		}
	}

	instConfig, err := s.AppConfig.InstallationConfig(strconv.FormatInt(installID, 10))
	if err != nil {
		return &httpjson.Response{
			Status: http.StatusInternalServerError,
			Body:   httpjson.M{"error": err.Error()},
		}
	}

	ctx := r.Context()
	ghClient := github.NewClient(instConfig.Client(ctx))

	repo, _, err := ghClient.Repositories.Get(ctx, owner, repoName)
	if err != nil {
		return &httpjson.Response{
			Status: http.StatusInternalServerError,
			Body:   httpjson.M{"error": err.Error()},
		}
	}

	if repo.GetPrivate() {
		// Do not allow searches on private repo because I need to implement
		// searcher authentication to do it safely.
		return &httpjson.Response{
			Status: http.StatusForbidden,
			Body:   httpjson.M{"error": "private repo"},
		}
	}

	repoIssues, err := s.getCachedRepoIssues(ctx, owner, repoName)
	if err != nil {
		return &httpjson.Response{
			Status: http.StatusInternalServerError,
			Body:   httpjson.M{"error": err.Error()},
		}
	}

	return &httpjson.Response{
		Status: http.StatusOK,
		Body: httpjson.M{
			"install_id": installID,
			"issues":     repoIssues,
		},
	}
}
