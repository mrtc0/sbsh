package builtins

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrtc0/sbsh/sandbox/command"
)

// capturedRequest is what the test server saw, so that the tests can assert on
// the request curl built rather than only on what came back.
type capturedRequest struct {
	method string
	header http.Header
	body   string
}

func curlTestServer(t *testing.T, got *capturedRequest) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		*got = capturedRequest{method: r.Method, header: r.Header.Clone(), body: string(body)}

		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "/", http.StatusFound)
		case "/missing":
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, "not found body")
		default:
			w.Header().Set("X-Test", "1")
			io.WriteString(w, "hello")
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func Test_curl(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		args         []string
		path         string
		wantStdout   string
		wantContains []string
		wantFile     string
		wantExit     int
	}{
		"prints the body": {
			path:       "/",
			wantStdout: "hello",
		},
		"-o writes to a file instead of standard output": {
			args:       []string{"-o", "out.txt"},
			path:       "/",
			wantStdout: "",
			wantFile:   "hello",
		},
		"-i prints the status line and the headers": {
			args:         []string{"-i"},
			path:         "/",
			wantContains: []string{"HTTP/1.1 200 OK", "X-Test: 1", "hello"},
		},
		"-I asks for the headers only": {
			args:         []string{"-I"},
			path:         "/",
			wantContains: []string{"HTTP/1.1 200 OK", "X-Test: 1"},
		},
		"a 4xx body is printed like any other": {
			path:       "/missing",
			wantStdout: "not found body",
		},
		"-f prints nothing and exits 22 on a 4xx": {
			args:       []string{"-f"},
			path:       "/missing",
			wantStdout: "",
			wantExit:   curlFailExit,
		},
		"-L follows the redirect": {
			args:       []string{"-L"},
			path:       "/redirect",
			wantStdout: "hello",
		},
		"without -L the redirect itself is the response": {
			args:         []string{"-i"},
			path:         "/redirect",
			wantContains: []string{"302 Found", "Location: /"},
		},
		"-s is accepted": {
			args:       []string{"-s"},
			path:       "/",
			wantStdout: "hello",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var got capturedRequest
			srv := curlTestServer(t, &got)

			env, stdout, _ := NewTestEnv(t, "/work")
			env.HTTP = &http.Client{}

			env.Args = append(tc.args, srv.URL+tc.path)
			err := curl(context.Background(), env)

			if tc.wantExit != 0 {
				var ee *command.ExitError
				require.ErrorAs(t, err, &ee)
				assert.Equal(t, tc.wantExit, ee.Code, "exit code")
			} else {
				require.NoError(t, err)
			}

			if len(tc.wantContains) > 0 {
				for _, want := range tc.wantContains {
					assert.Contains(t, stdout.String(), want)
				}
			} else {
				assert.Equal(t, tc.wantStdout, stdout.String(), "stdout")
			}

			if tc.wantFile != "" {
				b, readErr := afero.ReadFile(env.FS, "/work/out.txt")
				require.NoError(t, readErr)
				assert.Equal(t, tc.wantFile, string(b))
			}
		})
	}
}

func Test_curlRequest(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		args            []string
		stdin           string
		seed            map[string]string
		wantMethod      string
		wantBody        string
		wantContentType string
		wantHeader      map[string]string
	}{
		"GET by default": {
			wantMethod: http.MethodGet,
		},
		"-I sends HEAD": {
			args:       []string{"-I"},
			wantMethod: http.MethodHead,
		},
		"-X chooses the method": {
			args:       []string{"-X", "DELETE"},
			wantMethod: http.MethodDelete,
		},
		"-d implies POST and sets a form content type": {
			args:            []string{"-d", "a=1"},
			wantMethod:      http.MethodPost,
			wantBody:        "a=1",
			wantContentType: "application/x-www-form-urlencoded",
		},
		"-X wins over the POST that -d implies": {
			args:       []string{"-X", "PUT", "-d", "a=1"},
			wantMethod: http.MethodPut,
			wantBody:   "a=1",
		},
		"-d @FILE reads the body from the sandbox filesystem": {
			args:       []string{"-d", "@body.json"},
			seed:       map[string]string{"/work/body.json": `{"k":"v"}`},
			wantMethod: http.MethodPost,
			wantBody:   `{"k":"v"}`,
		},
		"-d @- reads the body from standard input": {
			args:       []string{"-d", "@-"},
			stdin:      "from stdin",
			wantMethod: http.MethodPost,
			wantBody:   "from stdin",
		},
		"-H adds a header": {
			args:       []string{"-H", "X-Token: abc"},
			wantMethod: http.MethodGet,
			wantHeader: map[string]string{"X-Token": "abc"},
		},
		"-H overrides the content type -d would set": {
			args:            []string{"-d", "a=1", "-H", "Content-Type: application/json"},
			wantMethod:      http.MethodPost,
			wantBody:        "a=1",
			wantContentType: "application/json",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var got capturedRequest
			srv := curlTestServer(t, &got)

			env, _, _ := NewTestEnv(t, "/work")
			env.HTTP = &http.Client{}
			for path, body := range tc.seed {
				mustWrite(t, env.FS, path, body)
			}
			if tc.stdin != "" {
				env.Stdin = strings.NewReader(tc.stdin)
			}

			env.Args = append(tc.args, srv.URL+"/")
			require.NoError(t, curl(context.Background(), env))

			assert.Equal(t, tc.wantMethod, got.method, "method")
			assert.Equal(t, tc.wantBody, got.body, "body")
			if tc.wantContentType != "" {
				assert.Equal(t, tc.wantContentType, got.header.Get("Content-Type"))
			}
			for name, want := range tc.wantHeader {
				assert.Equal(t, want, got.header.Get(name), "header %s", name)
			}
		})
	}
}

func Test_curlRejects(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		args       []string
		noClient   bool
		wantErrMsg string
	}{
		"no URL": {args: nil, wantErrMsg: "usage"},
		"more than one URL": {
			args:       []string{"http://a.example/", "http://b.example/"},
			wantErrMsg: "usage",
		},
		"a header without a colon": {
			args:       []string{"-H", "X-Token", "http://a.example/"},
			wantErrMsg: "invalid header",
		},
		"an unknown flag": {
			args:       []string{"-Z", "http://a.example/"},
			wantErrMsg: "invalid option",
		},
		"no network policy": {
			args:       []string{"http://a.example/"},
			noClient:   true,
			wantErrMsg: "network access is not permitted",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			env, _, _ := NewTestEnv(t, "/work")
			if !tc.noClient {
				env.HTTP = &http.Client{}
			}

			env.Args = tc.args
			err := curl(context.Background(), env)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErrMsg)
		})
	}
}
