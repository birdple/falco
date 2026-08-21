package unit

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/birdple/falco/internal/api/handlers"
	"github.com/birdple/falco/tests/mocks"
)

func signHandler(t *testing.T) *handlers.Handler {
	t.Helper()
	cfg := testConfig()
	// La clave va en hex y el tamaño de firma es el default real (32); con
	// cualquiera de los dos mal, SignURL devuelve 500 y el test no probaría nada.
	cfg.Security.HMACKey = "6465616462656566303132333435363738396162636465663031323334353637"
	cfg.Security.HMACSignatureSize = 32
	return handlers.NewHandler(cfg, new(mocks.MockStorageBackend), new(mocks.MockImageProcessor), time.Now())
}

func postSign(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sign", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	signHandler(t).HandleSignURL(rec, req)
	return rec
}

// Un error de decode y un path faltante son causas distintas y tienen que
// reportarse distinto. Estaban colapsados en un solo `err != nil || Path == ""`,
// así que un campo desconocido salía como "path is required" aunque el caller
// sí hubiera mandado path — y con json/v2, que es case-sensitive y rechaza
// campos desconocidos, ese caso pasó de inalcanzable a común.
func TestHandleSignURL_DecodeErrorNoSeDisfrazaDePathFaltante(t *testing.T) {
	casos := []struct {
		nombre string
		body   string
		codigo string
		porQue string
	}{
		{
			nombre: "campo desconocido",
			body:   `{"path":"/api/v1/images/abc","expires_in":600,"campo_inventado":1}`,
			codigo: "INVALID_JSON",
			porQue: "jsonx.Strict rechaza el documento entero; el path sí venía",
		},
		{
			nombre: "mayuscula que no coincide con el tag",
			body:   `{"Path":"/api/v1/images/abc","expires_in":600}`,
			codigo: "INVALID_JSON",
			porQue: "json/v2 es case-sensitive donde v1 no lo era",
		},
		{
			nombre: "json malformado",
			body:   `{"path":`,
			codigo: "INVALID_JSON",
			porQue: "no es un documento",
		},
		{
			nombre: "path ausente de verdad",
			body:   `{"expires_in":600}`,
			codigo: "INVALID_REQUEST",
			porQue: "este sí es el caso que 'path is required' describe",
		},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			rec := postSign(t, c.body)
			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Contains(t, rec.Body.String(), c.codigo, c.porQue)
		})
	}
}

func TestHandleSignURL_BodyValidoDevuelveURLFirmada(t *testing.T) {
	rec := postSign(t, `{"path":"/api/v1/images/abc","expires_in":600}`)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"signed_url"`)
	assert.Contains(t, rec.Body.String(), "sig=")
}
