package authn

import (
	"context"
	"net/http"

	"github.com/goodtek-xyz/openllms/internal/apikey"
	"github.com/goodtek-xyz/openllms/internal/store"
)

type ctxKey int

const authCtxKey ctxKey = 1

func WithAuth(st *store.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tok, err := apikey.ParseBearer(r.Header.Get("Authorization"))
			if err != nil {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			ac, err := st.LookupByPlaintext(r.Context(), tok)
			if err != nil {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			ctx := context.WithValue(r.Context(), authCtxKey, ac)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func FromContext(ctx context.Context) (*store.AuthContext, bool) {
	ac, ok := ctx.Value(authCtxKey).(*store.AuthContext)
	return ac, ok
}
