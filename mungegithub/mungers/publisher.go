/*
Copyright 2016 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package mungers

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/golang/glog"
	"github.com/spf13/cobra"
	"k8s.io/contrib/mungegithub/features"
	"k8s.io/contrib/mungegithub/github"
	"k8s.io/kubernetes/pkg/util/errors"
)

// coordinate of a piece of code
type coordinate struct {
	repo   string
	branch string
	// dir from repo root
	dir string
}

func (c coordinate) String() string {
	return fmt.Sprintf("[repository %s, branch %s, subdir %s]", c.repo, c.branch, c.dir)
}

// a collection of publishing rules for a single destination repo
type repoRules struct {
	dstRepo  string
	srcToDst map[coordinate]coordinate
}

// PublisherMunger publishes content from one repository to another one.
type PublisherMunger struct {
	// Command for the 'publisher' munger to run periodically.
	PublishCommand string
	// base for all repos
	baseDir string
	// location to write the netrc file needed for github authentication
	netrcDir     string
	reposrules   []repoRules
	features     *features.Features
	githubConfig *github.Config
}

func init() {
	publisherMunger := &PublisherMunger{}
	RegisterMungerOrDie(publisherMunger)
}

// Name is the name usable in --pr-mungers
func (p *PublisherMunger) Name() string { return "publisher" }

// RequiredFeatures is a slice of 'features' that must be provided
func (p *PublisherMunger) RequiredFeatures() []string { return []string{features.RepoFeatureName} }

// Initialize will initialize the munger
func (p *PublisherMunger) Initialize(config *github.Config, features *features.Features) error {
	p.baseDir = features.Repos.BaseDir
	if len(p.baseDir) == 0 {
		glog.Fatalf("--repo-dir is required with selected munger(s)")
	}
	clientGo := repoRules{
		dstRepo: "client-go",
		srcToDst: map[coordinate]coordinate{
			// rule for the client-go master branch
			coordinate{repo: config.Project, branch: "master", dir: "staging/src/k8s.io/client-go"}: coordinate{repo: "client-go", branch: "master", dir: "./"},
			// rule for the client-go release-2.0 branch
			coordinate{repo: config.Project, branch: "release-1.5", dir: "staging/src/k8s.io/client-go"}: coordinate{repo: "client-go", branch: "release-2.0", dir: "./"},
		},
	}
	p.reposrules = []repoRules{clientGo}
	glog.Infof("pulisher munger rules: %#v\n", p.reposrules)
	p.features = features
	p.githubConfig = config
	return nil
}

// git clone dstURL to dst
func clone(dst string, dstURL string) error {
	err := exec.Command("rm", "-rf", dst).Run()
	if err != nil {
		return err
	}
	err = exec.Command("mkdir", "-p", dst).Run()
	if err != nil {
		return err
	}
	err = exec.Command("git", "clone", dstURL, dst).Run()
	if err != nil {
		return err
	}
	return nil
}

// construct checks out the source repo, copy the contents to the destination,
// returns a commit message snippet and error.
func construct(base, org string, src, dst coordinate) (string, error) {
	srcRepoRoot := filepath.Join(base, src.repo)
	srcDir := filepath.Join(base, src.repo, src.dir)
	dstRepoRoot := filepath.Join(base, dst.repo)
	curDir, err := os.Getwd()
	if err != nil {
		glog.Infof("Getwd failed")
		return "", err
	}
	if err = os.Chdir(srcRepoRoot); err != nil {
		glog.Infof("Chdir to srcRepoRoot %s failed", srcRepoRoot)
		return "", err
	}
	if err = exec.Command("git", "checkout", src.branch).Run(); err != nil {
		glog.Infof("git checkout %s failed", src.branch)
		return "", err
	}
	out, err := exec.Command("git", "rev-parse", "HEAD").CombinedOutput()
	if err != nil {
		glog.Infof("git rev-parse failed")
		return "", err
	}
	commitHash := string(out)
	if err = os.Chdir(dstRepoRoot); err != nil {
		glog.Infof("Chdir to dstRepoRoot %s failed", dstRepoRoot)
		return "", err
	}
	// TODO: this makes construct() specific for client-go. This keeps
	// README.md, CHANGELOG.md, examples folder, .github folder in the
	// client-go, rather than copying them from src.
	if out, err := exec.Command("sh", "-c", fmt.Sprintf(`\
find %s -depth -maxdepth 1 \( \
-name examples -o \
-name .github -o \
-name .git -o \
-name README.md -o \
-name CHANGELOG.md -o \
-path %s \) -prune \
-o -exec rm -rf {} +`, dst.dir, dst.dir)).CombinedOutput(); err != nil {
		glog.Infof("command \"find\" failed: %s", out)
		return "", err
	}
	if dst.dir == "./" {
		// don't copy the srcDir folder, just copy its contents
		err = exec.Command("cp", "-a", srcDir+"/.", dst.dir).Run()
	} else {
		err = exec.Command("cp", "-a", srcDir, dst.dir).Run()
	}
	if err != nil {
		glog.Infof("copy failed")
		return "", err
	}
	// rename _vendor to vendor
	if err = exec.Command("find", dst.dir, "-depth", "-name", "_vendor", "-type", "d", "-execdir", "mv", "{}", "vendor", ";").Run(); err != nil {
		glog.Infof("rename _vendor to vendor failed")
		return "", err
	}
	if err = os.Chdir(curDir); err != nil {
		glog.Infof("Chdir to curDir failed")
		return "", err
	}
	srcURL := fmt.Sprintf("https://github.com/%s/%s.git", org, src.repo)
	commitMessage := fmt.Sprintf("copied from %s, branch %s,\n", srcURL, src.branch)
	commitMessage += fmt.Sprintf("last commit is %s\n", commitHash)
	return commitMessage, nil
}

// EachLoop is called at the start of every munge loop
func (p *PublisherMunger) EachLoop() error {
	var errlist []error
Repos:
	for _, rules := range p.reposrules {
		// clone the destination repo
		dstDir := filepath.Join(p.baseDir, rules.dstRepo, "")
		dstURL := fmt.Sprintf("https://github.com/%s/%s.git", p.githubConfig.Org, rules.dstRepo)
		err := clone(dstDir, dstURL)
		if err != nil {
			glog.Errorf("Failed to clone %s.\nError: %s", dstURL, err)
			errlist = append(errlist, err)
			continue Repos
		} else {
			glog.Infof("Successfully clone %s", dstURL)
		}
		if err = os.Chdir(dstDir); err != nil {
			glog.Errorf("Failed to chdir to %s.\nError: %s", dstDir, err)
			errlist = append(errlist, err)
			continue Repos
		}
		// construct the repo's branches and subdirs
		for src, dst := range rules.srcToDst {
			var commitMessage = "published by bot\n(https://github.com/kubernetes/contrib/tree/master/mungegithub)\n\n"
			if err = exec.Command("git", "checkout", dst.branch).Run(); err != nil {
				glog.Errorf("Failed to checkout branch %s.\nError: %s", dst.branch, err)
				errlist = append(errlist, err)
				continue Repos
			}
			dstRepoRoot := filepath.Join(p.baseDir, dst.repo)
			snippet, err := construct(p.baseDir, p.githubConfig.Org, src, dst)
			if err != nil {
				glog.Errorf("Failed to construct %s.\nError: %s", dstRepoRoot, err)
				errlist = append(errlist, err)
				continue Repos
			} else {
				commitMessage += snippet
				glog.Infof("Successfully construct %s", filepath.Join(dstRepoRoot, dst.dir))
			}

			// publish the destination branch
			cmd := exec.Command("/publish.sh", filepath.Join(dstRepoRoot, dst.dir), dst.branch, p.githubConfig.Token(), p.netrcDir, strings.TrimSpace(commitMessage))
			output, err := cmd.CombinedOutput()
			if err != nil {
				glog.Errorf("Failed to publish %s.\nOutput: %s\nError: %s", dst, output, err)
				errlist = append(errlist, err)
				continue Repos
			} else {
				glog.Infof("Successfully publish %s: %s", dst, output)
			}
		}
	}
	return errors.NewAggregate(errlist)
}

// AddFlags will add any request flags to the cobra `cmd`
func (p *PublisherMunger) AddFlags(cmd *cobra.Command, config *github.Config) {
	cmd.Flags().StringVar(&p.netrcDir, "netrc-dir", "", "Location to write the netrc file needed for github authentication.")
}

// Munge is the workhorse the will actually make updates to the PR
func (p *PublisherMunger) Munge(obj *github.MungeObject) {}
