// 3mf-viewer は指定パス配下の 3MF / STL をブラウザでプレビューするローカルビューア。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"runtime/debug"
	"strconv"
	"syscall"
	"time"

	"github.com/m-tkg/3mf-viewer/internal/scan"
	"github.com/m-tkg/3mf-viewer/internal/server"
)

// defaultPort は --port 未指定時に最初に試す固定ポート。使用中なら空きポートに退避する。
const defaultPort = 3977

// version はビルド時に -ldflags "-X main.version=..." で上書きできる。
var version = ""

func getVersion() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}

const usage = `使い方: 3mf-viewer [オプション] [path]

<path> 配下 (サブディレクトリ含む) の .3mf / .stl をブラウザでプレビューする。
<path> にはディレクトリまたは単一ファイルを指定できる。省略時はカレントディレクトリ。終了は Ctrl+C。
画面の「フォルダを開く」から、ブラウザで選んだ別のフォルダも読み込める。

オプション:
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("3mf-viewer", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), usage)
		fs.PrintDefaults()
	}
	port := fs.Int("port", defaultPort, "待ち受けポート (未指定時は "+strconv.Itoa(defaultPort)+"、使用中なら空きポート)")
	host := fs.String("host", "127.0.0.1", "待ち受けアドレス (既定はローカルのみ)")
	noOpen := fs.Bool("no-open", false, "起動時にブラウザを開かない")
	interval := fs.Duration("interval", time.Second, "ファイル監視の間隔")
	showVersion := fs.Bool("version", false, "バージョンを表示して終了")

	// フラグと位置引数の混在 (3mf-viewer <path> --no-open) を許す。
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if *showVersion {
		fmt.Println("3mf-viewer", getVersion())
		return 0
	}
	if len(positional) > 1 {
		fs.Usage()
		return 2
	}
	target := "."
	if len(positional) == 1 {
		target = positional[0]
	}
	portSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "port" {
			portSet = true
		}
	})

	root, err := scan.NewRoot(target)
	if err != nil {
		fmt.Fprintln(os.Stderr, "エラー:", err)
		return 1
	}

	ln, err := listen(*host, *port, portSet)
	if err != nil {
		fmt.Fprintln(os.Stderr, "エラー:", err)
		return 1
	}
	loopback := isLoopback(*host)
	if !loopback {
		fmt.Fprintf(os.Stderr, "警告: %s で待ち受け中。ネットワーク上の他のホストからも %s 配下が閲覧できる\n", *host, root.Dir)
	}

	srv := server.New(server.Config{Root: root, Interval: *interval, Loopback: loopback, Version: getVersion()})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go srv.Run(ctx)

	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- httpSrv.Serve(ln) }()

	url := "http://" + displayAddr(ln.Addr().(*net.TCPAddr), *host) + "/"
	fmt.Printf("3mf-viewer: %s を %s で公開中 (Ctrl+C で終了)\n", root.Dir, url)
	if !*noOpen {
		if err := openBrowser(url); err != nil {
			fmt.Fprintln(os.Stderr, "ブラウザを開けなかった:", err)
		}
	}

	select {
	case <-ctx.Done():
	case err := <-errc:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(os.Stderr, "エラー:", err)
			return 1
		}
	}
	sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// SSE が接続中だと Shutdown が待たされるため、タイムアウト後は強制的に閉じる。
	if err := httpSrv.Shutdown(sctx); err != nil {
		_ = httpSrv.Close()
	}
	return 0
}

// listen は host:port で待ち受ける。port 未指定で使用中なら空きポート (0) を使う。
func listen(host string, port int, explicit bool) (net.Listener, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err == nil {
		return ln, nil
	}
	if explicit {
		return nil, err
	}
	return net.Listen("tcp", net.JoinHostPort(host, "0"))
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// displayAddr はユーザーに見せる / 開く URL 用のホスト:ポートを返す。
func displayAddr(a *net.TCPAddr, host string) string {
	h := host
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		h = "127.0.0.1"
	}
	return net.JoinHostPort(h, strconv.Itoa(a.Port))
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
