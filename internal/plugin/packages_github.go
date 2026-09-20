package plugin

import (
	"context"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/plugingithub"
)

type packageFetcher func(context.Context, string, string) (plugingithub.Result, error)

// SetPackageFetcher supplies the trusted GitHub transport before serving requests.
// A nil fetcher restores the production HTTPS-only, public-address-pinned importer.
func (s *Service) SetPackageFetcher(fetch func(context.Context, string, string) (plugingithub.Result, error)) {
	s.packageFetcher = fetch
}

func (s *Service) fetchPackage(ctx context.Context, repositoryURL, ref string) (plugingithub.Result, error) {
	if s.packageFetcher != nil {
		return s.packageFetcher(ctx, repositoryURL, ref)
	}
	return plugingithub.Fetch(ctx, repositoryURL, ref)
}

func (s *Service) PreviewGitHubPackage(ctx context.Context, actor application.Principal, repositoryURL, ref string) (model.PluginPackagePreview, error) {
	return s.previewGitHubPackage(ctx, actor, repositoryURL, ref, s.fetchPackage)
}
func (s *Service) previewGitHubPackage(ctx context.Context, actor application.Principal, repositoryURL, ref string, fetch packageFetcher) (model.PluginPackagePreview, error) {
	if err := s.requirePackagePermission(actor, "plugins.read"); err != nil {
		return model.PluginPackagePreview{}, err
	}
	result, err := fetch(ctx, repositoryURL, ref)
	if err != nil {
		return model.PluginPackagePreview{}, err
	}
	preview, err := s.PreviewPackage(ctx, actor, result.Archive)
	if err == nil {
		preview.Source = &model.PluginPackageSource{Kind: "github", Repository: result.Source.Owner + "/" + result.Source.Repo, Commit: result.Source.Commit}
	}
	return preview, err
}

// Installation refetches the reviewed commit, never the mutable branch or tag.
func (s *Service) InstallGitHubPackage(ctx context.Context, actor application.Principal, repositoryURL, commit, expectedSHA256 string, confirm bool) (model.PluginPackageInstallResult, error) {
	return s.installGitHubPackage(ctx, actor, repositoryURL, commit, expectedSHA256, confirm, s.fetchPackage)
}
func (s *Service) installGitHubPackage(ctx context.Context, actor application.Principal, repositoryURL, commit, expectedSHA256 string, confirm bool, fetch packageFetcher) (model.PluginPackageInstallResult, error) {
	var empty model.PluginPackageInstallResult
	if err := s.requirePackagePermission(actor, "plugins.publish"); err != nil {
		return empty, err
	}
	if !confirm || !packageCommit.MatchString(commit) || len(expectedSHA256) != 64 {
		return empty, Coded(codeInvalidInput, "confirm, immutable commit and expected_sha256 are required")
	}
	result, err := fetch(ctx, repositoryURL, commit)
	if err != nil {
		return empty, err
	}
	if result.Source.Commit != commit {
		return empty, ErrConflict
	}
	return s.InstallPackage(ctx, actor, result.Archive, expectedSHA256, confirm, PackageSource{Kind: "github", Repository: result.Source.Owner + "/" + result.Source.Repo, Commit: result.Source.Commit})
}
