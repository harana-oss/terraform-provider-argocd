package argocd

import (
	"context"
	"fmt"

	"github.com/Masterminds/semver"
	"github.com/argoproj/argo-cd/pkg/apiclient"
	"github.com/argoproj/argo-cd/pkg/apiclient/application"
	"github.com/argoproj/argo-cd/pkg/apiclient/cluster"
	"github.com/argoproj/argo-cd/pkg/apiclient/project"
	"github.com/argoproj/argo-cd/pkg/apiclient/repocreds"
	"github.com/argoproj/argo-cd/pkg/apiclient/repository"
	"github.com/argoproj/argo-cd/pkg/apiclient/version"
	"github.com/argoproj/argo-cd/util/io"
	"github.com/golang/protobuf/ptypes/empty"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

const (
	featureApplicationLevelSyncOptions = iota
	featureRepositoryGet
	featureTokenIDs
)

var (
	featureVersionConstraintsMap = map[int]*semver.Version{
		featureApplicationLevelSyncOptions: semver.MustParse("1.5.0"),
		featureRepositoryGet:               semver.MustParse("1.6.0"),
		featureTokenIDs:                    semver.MustParse("1.5.3"),
	}
)

type ServerInterface struct {
	ApiClient            *apiclient.Client
	ApplicationClient    *application.ApplicationServiceClient
	ClusterClient        *cluster.ClusterServiceClient
	ProjectClient        *project.ProjectServiceClient
	RepositoryClient     *repository.RepositoryServiceClient
	RepoCredsClient      *repocreds.RepoCredsServiceClient
	ServerVersion        *semver.Version
	ServerVersionMessage *version.VersionMessage
	ProviderData         *schema.ResourceData
}

func (p *ServerInterface) initClients() error {
	d := p.ProviderData

	if p.ApiClient == nil {
		apiClient, err := initApiClient(d)
		if err != nil {
			return err
		}
		p.ApiClient = &apiClient
	}

	if p.ClusterClient == nil {
		_, clusterClient, err := (*p.ApiClient).NewClusterClient()
		if err != nil {
			return err
		}
		p.ClusterClient = &clusterClient
	}

	if p.ApplicationClient == nil {
		_, applicationClient, err := (*p.ApiClient).NewApplicationClient()
		if err != nil {
			return err
		}
		p.ApplicationClient = &applicationClient
	}

	if p.ProjectClient == nil {
		_, projectClient, err := (*p.ApiClient).NewProjectClient()
		if err != nil {
			return err
		}
		p.ProjectClient = &projectClient
	}

	if p.RepositoryClient == nil {
		_, repositoryClient, err := (*p.ApiClient).NewRepoClient()
		if err != nil {
			return err
		}
		p.RepositoryClient = &repositoryClient
	}

	if p.RepoCredsClient == nil {
		_, repoCredsClient, err := (*p.ApiClient).NewRepoCredsClient()
		if err != nil {
			return err
		}
		p.RepoCredsClient = &repoCredsClient
	}

	acCloser, versionClient, err := (*p.ApiClient).NewVersionClient()
	if err != nil {
		return err
	}
	defer io.Close(acCloser)

	serverVersionMessage, err := versionClient.Version(context.Background(), &empty.Empty{})
	if err != nil {
		return err
	}
	if serverVersionMessage == nil {
		return fmt.Errorf("could not get server version information")
	}
	p.ServerVersionMessage = serverVersionMessage
	serverVersion, err := semver.NewVersion(serverVersionMessage.Version)
	if err != nil {
		return fmt.Errorf("could not parse server semantic version: %s", serverVersionMessage.Version)
	}
	p.ServerVersion = serverVersion

	return nil
}

// Checks that a specific feature is available for the current ArgoCD server version.
// 'feature' argument must match one of the predefined feature* constants.
func (p ServerInterface) isFeatureSupported(feature int) (bool, error) {
	versionConstraint, ok := featureVersionConstraintsMap[feature]
	if !ok {
		return false, fmt.Errorf("feature constraint is not handled by the provider")
	}
	if i := versionConstraint.Compare(p.ServerVersion); i == 1 {
		return false, nil
	}
	return true, nil
}
