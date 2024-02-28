package labeler

import (
	"net/http"

	"github.com/coder/labeler/httpjson"
	"github.com/go-chi/chi/v5"
	"github.com/google/go-github/v59/github"
	"github.com/sashabaranov/go-openai"
)

type Server struct {
	OpenAI *openai.Client
	Client *github.Client

	router *chi.Mux
}

func (s *Server) Init() {
	s.router = chi.NewRouter()
	s.router.Mount("/infer", httpjson.Handler(s.infer))
}

func (s *Server) infer(w http.ResponseWriter, r *http.Request) *httpjson.Response {
	var (
		user  = r.URL.Query().Get("user")
		repo  = r.URL.Query().Get("repo")
		issue = r.URL.Query().Get("issue")
	)

	issues, _, err := s.Client.Issues.ListByRepo(
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
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}
