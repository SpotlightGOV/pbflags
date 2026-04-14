// Package adminclient provides an authenticated Connect RPC client for the
// pbflags admin API, suitable for use by the CLI.
package adminclient

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"connectrpc.com/connect"

	"github.com/SpotlightGOV/pbflags/gen/pbflags/v1/pbflagsv1connect"
	"github.com/SpotlightGOV/pbflags/internal/credentials"
)

// DefaultURL is the default admin API address.
const DefaultURL = "http://localhost:9200"

// New creates an authenticated FlagAdminServiceClient. It loads credentials
// from the standard lookup chain (env var, then credentials file) and injects
// them as headers on every request.
//
// The admin URL is resolved from the url parameter, PBFLAGS_ADMIN_URL env var,
// or DefaultURL, in that order.
func New(url string) (pbflagsv1connect.FlagAdminServiceClient, error) {
	if url == "" {
		url = os.Getenv("PBFLAGS_ADMIN_URL")
	}
	if url == "" {
		url = DefaultURL
	}

	creds, err := credentials.Load()
	if err != nil {
		return nil, fmt.Errorf("load credentials: %w", err)
	}

	httpClient := http.DefaultClient
	var opts []connect.ClientOption
	if creds.Token != "" {
		opts = append(opts, connect.WithInterceptors(&authInterceptor{creds: creds}))
	}

	return pbflagsv1connect.NewFlagAdminServiceClient(httpClient, url, opts...), nil
}

// authInterceptor injects Authorization and X-Actor headers.
type authInterceptor struct {
	creds credentials.Credentials
}

func (a *authInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		req.Header().Set("Authorization", "Bearer "+a.creds.Token)
		if a.creds.Actor != "" {
			req.Header().Set("X-Actor", a.creds.Actor)
		}
		return next(ctx, req)
	}
}

func (a *authInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (a *authInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}
