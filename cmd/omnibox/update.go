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
	// The token saved in the panel for updates, readable from an administrator's terminal.
	if t, ok := loadToken(platform.DataDir()); ok {
		return t.Token, nil
	}
	fmt.Print("GitHub token с правом чтения репозитория aafnx/omnibox-ai (ввод скрыт): ")
	v, err := platform.ReadSecret()
	if v == "" {
		return "", errors.New("Token не введён: обновление отменено.")
	}
	return v, err
}

// runUpdate is "ai update [версия]": it downloads the release, checks it and runs its installer,
// which updates the program with rollback and keeps every setting; an older version is a
// rollback. "ai update --check" only tells whether a newer release exists, "ai update --list"
// lists the versions to choose from.
func runUpdate(args []string, u updater, stdout, stderr io.Writer) int {
	version, check, list := "", false, false
	if len(args) == 1 && args[0] == "--check" {
		check, args = true, nil
	}
	if len(args) == 1 && args[0] == "--list" {
		list, args = true, nil
	}
	switch len(args) {
	case 0:
	case 1:
		version = args[0]
	default:
		fmt.Fprintln(stderr, "Используйте: ai update [версия] или ai update --check")
		return 2
	}
	token, err := u.Token()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	up := u.New(token)
	ctx := context.Background()
	if list {
		return listVersions(ctx, up, u.Current, stdout, stderr)
	}
	release, err := up.Find(ctx, version)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if check {
		if !newer(release.Version(), u.Current) {
			fmt.Fprintf(stdout, "Установлена последняя версия %s.\n", u.Current)
			return 0
		}
		fmt.Fprintf(stdout, "Доступна версия %s (установлена %s). Обновить: ai update\n", release.Version(), u.Current)
		if release.Notes != "" {
			fmt.Fprintf(stdout, "\n%s\n", release.Notes)
		}
		return 0
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
	if newer(u.Current, release.Version()) {
		fmt.Fprintf(stdout, "Откат с %s на %s: настройки, чаты и модели остаются; новые настройки, которых нет в %s, не применяются.\n", u.Current, release.Version(), release.Version())
	}
	fmt.Fprintf(stdout, "Выпуск проверен по SHA256. Установка %s (при сбое возвращается %s)…\n", release.Version(), u.Current)
	return u.Install(exe, stdout, stderr)
}

// listVersions prints the releases, the installed one marked, and how to install one.
func listVersions(ctx context.Context, up *selfupdate.Updater, current string, stdout, stderr io.Writer) int {
	list, err := up.List(ctx)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if len(list) == 0 {
		fmt.Fprintln(stdout, "Выпусков нет.")
		return 0
	}
	for _, r := range list {
		mark := "  "
		if r.Version == current {
			mark = "* "
		}
		line := mark + r.Version
		if !r.Published.IsZero() {
			line += "  " + r.Published.Local().Format("2006-01-02")
		}
		if r.Prerelease {
			line += "  (предварительный)"
		}
		if r.Summary != "" {
			line += "  " + r.Summary
		}
		fmt.Fprintln(stdout, line)
	}
	fmt.Fprintf(stdout, "\n* — установлена (%s). Установить или откатиться: ai update <версия>\n", current)
	return 0
}
