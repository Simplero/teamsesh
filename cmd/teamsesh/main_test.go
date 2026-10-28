package main

import "testing"

func TestSameRepoAcrossURLStyles(t *testing.T) {
	same := [][2]string{
		{"git@github.com:Simplero/teamsesh.git", "https://github.com/simplero/teamsesh"},
		{"ssh://git@github.com/Simplero/teamsesh.git", "git@github.com:Simplero/teamsesh"},
	}
	for _, p := range same {
		if !sameRepo(p[0], p[1]) {
			t.Errorf("%s and %s should match", p[0], p[1])
		}
	}
	if sameRepo("git@github.com:Simplero/teamsesh.git", "git@github.com:Simplero/backtrace.git") || sameRepo("", "") {
		t.Error("different or empty repos matched")
	}
}
