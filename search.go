package labeler

import (
	"context"
	"log/slog"
	"math"
	"net/http"
	"sort"
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
		AND user = @owner AND state = 'open' AND pull_request = false
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

func cosineSimilarity(a, b []float64) float64 {
	var dot float64
	var magA float64
	var magB float64
	for i := range a {
		dot += a[i] * b[i]
		magA += a[i] * a[i]
		magB += b[i] * b[i]
	}

	magA = math.Sqrt(magA)
	magB = math.Sqrt(magB)

	return dot / (magA * magB)
}

type bqIssueWithSimilarity struct {
	*BqIssue
	Similarity float64
}

// TODO: I can parallelize this search or use coder/hnsw in the future.
func bruteSearch(issues []*BqIssue, searchQuery []float64) []*bqIssueWithSimilarity {
	var r1 []*bqIssueWithSimilarity
	for _, issue := range issues {
		similarity := cosineSimilarity(issue.Embedding, searchQuery)
		r1 = append(r1, &bqIssueWithSimilarity{
			BqIssue:    issue,
			Similarity: similarity,
		})
	}
	sort.Slice(r1, func(i, j int) bool {
		return r1[i].Similarity > r1[j].Similarity
	})

	return r1
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

	searchQuery := r.URL.Query().Get("q")
	if searchQuery == "" {
		return &httpjson.Response{
			Status: http.StatusBadRequest,
			Body:   httpjson.M{"error": "missing q"},
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

	if len(repoIssues) == 0 {
		return &httpjson.Response{
			Status: http.StatusNotFound,
			Body:   httpjson.M{"error": "no issues found"},
		}
	}

	searchEmbedding, err := s.OpenAI.CreateEmbeddings(
		ctx,
		&openai.EmbeddingRequestStrings{
			Input: []string{searchQuery},
			Model: openai.SmallEmbedding3,
		},
	)
	if err != nil {
		return &httpjson.Response{
			Status: http.StatusInternalServerError,
			Body:   httpjson.M{"error": err.Error()},
		}
	}

	var searchEmbedding64 []float64
	for _, f := range searchEmbedding.Data[0].Embedding {
		searchEmbedding64 = append(searchEmbedding64, float64(f))
	}

	similarIssues := bruteSearch(repoIssues, searchEmbedding64)
	const maxResults = 100
	if len(similarIssues) > maxResults {
		similarIssues = similarIssues[:maxResults]
	}

	return &httpjson.Response{
		Status: http.StatusOK,
		Body: httpjson.M{
			"install_id": installID,
			"issues":     similarIssues,
		},
	}
}
