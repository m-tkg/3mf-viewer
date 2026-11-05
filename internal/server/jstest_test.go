package server

import (
	"os/exec"
	"testing"
)

// フロントの純粋ロジック (static/local.js) を node の組み込みテストランナーで検証する。node が無ければ skip。
func TestFrontendLogic(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node が無いので skip")
	}
	out, err := exec.Command(node, "--test", "jstest/").CombinedOutput()
	if err != nil {
		t.Fatalf("node --test 失敗: %v\n%s", err, out)
	}
}
