package builtins

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// curlFailExit is the exit code curl uses for -f, so that a script written
// against the real thing reads the same result here.
const curlFailExit = 22

// curl transfers a URL over HTTP.
//
//	-X, --request METHOD  use METHOD instead of GET
//	-H, --header LINE     add a request header, e.g. "Accept: application/json"
//	-d, --data DATA       send DATA as the request body, or the contents of FILE
//	                      for @FILE and standard input for @-. Implies POST
//	-o, --output FILE     write to FILE instead of standard output
//	-i, --include         print the status line and the response headers too
//	-I, --head            send HEAD and print the status line and the response headers
//	-L, --location        follow redirects
//	-f, --fail            print nothing and exit 22 on a 4xx or 5xx response
//	-s, --silent          accepted and ignored; there is no progress meter to hide
//	curl [options] URL
//
// Every request goes through the client the sandbox's network policy built. A
// sandbox configured without a policy has no client, and curl says so rather
// than reaching the network by some other route.
func curl(ctx context.Context, env *Env, args []string) error {
	fs := NewFlagSet()
	method := fs.String("", "-X", "--request")
	headers := fs.StringList("-H", "--header")
	data := fs.String("", "-d", "--data")
	output := fs.String("", "-o", "--output")
	include := fs.Bool("-i", "--include")
	head := fs.Bool("-I", "--head")
	location := fs.Bool("-L", "--location")
	fail := fs.Bool("-f", "--fail")
	fs.Bool("-s", "--silent")

	operands, err := fs.Parse(args)
	if err != nil {
		return err
	}
	if len(operands) != 1 {
		return errors.New("usage: curl [options] URL")
	}
	if env.HTTP == nil {
		return errors.New("network access is not permitted")
	}

	var body io.Reader
	if fs.Seen("-d") {
		payload, err := curlData(env, *data)
		if err != nil {
			return err
		}
		body = bytes.NewReader(payload)
	}

	req, err := curlRequest(ctx, operands[0], curlOptions{
		method:   *method,
		headers:  *headers,
		body:     body,
		wantHead: *head,
	})
	if err != nil {
		return err
	}

	// A copy so that the redirect choice is this call's alone: the client is
	// shared by every command in the sandbox. It keeps the same transport, and
	// with it the same policy.
	client := *env.HTTP
	if !*location {
		client.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}

	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	if *fail && res.StatusCode >= http.StatusBadRequest {
		fmt.Fprintf(env.HC.Stderr, "curl: the requested URL returned error: %s\n", res.Status)
		return exit(curlFailExit)
	}

	withHeaders := *include || *head
	if *output == "" {
		return writeResponse(env.HC.Stdout, res, withHeaders)
	}

	f, err := env.FS.OpenFile(env.Abs(*output), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if err := writeResponse(f, res, withHeaders); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

type curlOptions struct {
	method  string
	headers []string
	// body is nil when the request has none, which is also what makes an
	// unqualified request a GET rather than a POST.
	body     io.Reader
	wantHead bool
}

func curlRequest(ctx context.Context, url string, opts curlOptions) (*http.Request, error) {
	method := opts.method
	if method == "" {
		switch {
		case opts.wantHead:
			method = http.MethodHead
		case opts.body != nil:
			method = http.MethodPost
		default:
			method = http.MethodGet
		}
	}

	req, err := http.NewRequestWithContext(ctx, method, url, opts.body)
	if err != nil {
		return nil, err
	}
	if opts.body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	for _, h := range opts.headers {
		name, value, ok := strings.Cut(h, ":")
		if !ok || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("invalid header %q, want \"Name: value\"", h)
		}
		req.Header.Set(strings.TrimSpace(name), strings.TrimSpace(value))
	}
	return req, nil
}

// curlData resolves the -d value: @FILE reads the file, @- reads standard
// input, and anything else is the literal body.
func curlData(env *Env, data string) ([]byte, error) {
	if !strings.HasPrefix(data, "@") {
		return []byte(data), nil
	}
	return readSource(env, strings.TrimPrefix(data, "@"))
}

func writeResponse(w io.Writer, res *http.Response, withHeaders bool) error {
	if withHeaders {
		if _, err := fmt.Fprintf(w, "%s %s\r\n", res.Proto, res.Status); err != nil {
			return err
		}
		if err := res.Header.Write(w); err != nil {
			return err
		}
		if _, err := io.WriteString(w, "\r\n"); err != nil {
			return err
		}
	}
	_, err := io.Copy(w, res.Body)
	return err
}

func init() {
	Register("curl", curl)
}
