package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	omnibox "github.com/aafnx/omnibox-ai"
	"github.com/aafnx/omnibox-ai/internal/platform"
	"github.com/aafnx/omnibox-ai/internal/selfupdate"
)

// updater is what "ai update" needs; tests replace each part.
type updater struct {
	Token   func() (string, error)
	New     func(token string) *selfupdate.Updater
	Install func(exe string, stdout, stderr io.Writer) int
	TempDir func() (string, error)
	Current string
}

func defaultUpdater() updater {
	return updater{
		Token: githubToken,
		New:   func(token string) *selfupdate.Updater { return &selfupdate.Updater{Token: token} },
		Install: func(exe string, stdout, stderr io.Writer) int {
			cmd := exec.Command(exe, "install", "--yes")
			cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, stdout, stderr
			if err := cmd.Run(); err != nil {
				var exit *exec.ExitError
				if errors.As(err, &exit) {
					return exit.ExitCode()
				}
				fmt.Fprintln(stderr, "Установщик не запустился:", err)
				return 1
			}
			return 0
		},
		TempDir: func() (string, error) { return os.MkdirTemp("", "omnibox-update-") },
		Current: omnibox.Version,
	}
}

// githubToken takes the token from GH_TOKEN or GITHUB_TOKEN, then from a signed-in gh, and only
// then asks for it; it is kept in memory and never written or passed on a command line.
func githubToken() (string, error) {
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v, nil
		}
	}
	if gh, err := exec.LookPath("gh"); err == nil {
		if out, err := exec.Command(gh, "auth", "token").Output(); err == nil {
			if v := strings.TrimSpace(string(out)); v != "" {
				return v, nil
			}
		}
	}
	fmt.Print("GitHub token с правом чтения репозитория aafnx/omnibox-ai (ввод скрыт): ")
	v, err := platform.ReadSecret()
	if v == "" {
		return "", errors.New("Token не введён: обновление отменено.")
	}
	return v, err
}

// runUpdate is "ai update [версия]": it downloads the release, checks it and runs its installer,
// which updates the program with rollback and keeps every setting.
func runUpdate(args []string, u updater, stdout, stderr io.Writer) int {
	version := ""
	switch len(args) {
	case 0:
	case 1:
		version = args[0]
	default:
		fmt.Fprintln(stderr, "Используйте: ai update [версия]")
		return 2
	}
	token, err := u.Token()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	up := u.New(token)
	ctx := context.Background()
	release, err := up.Find(ctx, version)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if version == "" && release.Version() == u.Current {
		fmt.Fprintf(stdout, "Установлена последняя версия %s. Переустановить её: ai update %s\n", u.Current, u.Current)
		return 0
	}
	dir, err := u.TempDir()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)
	fmt.Fprintf(stdout, "Скачивание OmniBox AI %s…\n", release.Version())
	exe, err := up.Fetch(ctx, release, dir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "Выпуск проверен по SHA256. Установка %s (при сбое возвращается %s)…\n", release.Version(), u.Current)
	return u.Install(exe, stdout, stderr)
}
