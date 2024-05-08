package ghapi

import (
	"context"
	"encoding/json"
	"net/http"
)

// InstallIDForRepo returns the installation ID for a given repository.
func InstallIDForRepo(
	ctx context.Context,
	client *http.Client,
	owner, repo string,
) (int64, error) {
	resp, err := client.Get(
		"https://api.github.com/repos/" + owner + "/" + repo + "/installation",
	)
	if err != nil {
		return 0, err
	}

	var data struct {
		ID int64 `json:"id"`
	}

	err = json.NewDecoder(resp.Body).Decode(&data)
	if err != nil {
		return 0, err
	}

	return data.ID, nil
}
