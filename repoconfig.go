package labeler

import "fmt"

// repoConfig describes the labeler configuration for a repository.
type repoConfig struct {
	Enable  []string `yaml:"enable"`
	Disable []string `yaml:"disable"`
}

func (r *repoConfig) Validate() error {
	if len(r.Enable) == 0 && len(r.Disable) == 0 {
		return nil
	}
	if len(r.Enable) > 0 && len(r.Disable) > 0 {
		return fmt.Errorf("cannot have both enable and disable")
	}
	return nil
}
