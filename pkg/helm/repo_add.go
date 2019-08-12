package helm

import (
	"path/filepath"
	"sync"

	"github.com/pkg/errors"
	"helm.sh/helm/pkg/cli"
	"helm.sh/helm/pkg/getter"
	"helm.sh/helm/pkg/helmpath"
	"helm.sh/helm/pkg/repo"
)

var lock sync.Mutex

// AddBasicAuthRepository add a repo with basic auth
func AddBasicAuthRepository(name, url, username, password string) error {
	return addRepository(name, url, username, password, getHelmHome(), "", "", "", false)
}

// RemoveRepository remove a repo from helm
func RemoveRepository(name string) error {
	lock.Lock()
	defer lock.Unlock()

	home := getHelmHome()

	f, err := repo.LoadFile(home.RepositoryFile())
	if err != nil {
		return err
	}

	found := f.Remove(name)
	if found {
		return f.WriteFile(home.RepositoryFile(), 0644)
	}

	return nil
}

// addRepository add a repo and update index ( the repo already exist, we only need to update-index part)
func addRepository(name, url, username, password string, home helmpath.Home, certFile, keyFile, caFile string, noUpdate bool) error {
	lock.Lock()
	defer lock.Unlock()

	f, err := repo.LoadFile(home.RepositoryFile())
	if err != nil {
		return err
	}

	if noUpdate && f.Has(name) {
		return errors.Errorf("repository name (%s) already exists, please specify a different name", name)
	}

	cif := home.CacheIndex(name)
	absCif, err := filepath.Abs(cif)
	if err != nil {
		return err
	}

	c := repo.Entry{
		Name:     name,
		Cache:    absCif,
		URL:      url,
		Username: username,
		Password: password,
		CertFile: certFile,
		KeyFile:  keyFile,
		CAFile:   caFile,
	}

	settings := cli.EnvSettings{
		Home:  getHelmHome(),
		Debug: true,
	}

	r, err := repo.NewChartRepository(&c, getter.All(settings))
	if err != nil {
		return err
	}

	if err := r.DownloadIndexFile(home.Cache()); err != nil {
		return errors.Wrapf(err, "looks like %q is not a valid chart repository or cannot be reached", url)
	}

	f.Update(&c)

	return f.WriteFile(home.RepositoryFile(), 0644)
}
