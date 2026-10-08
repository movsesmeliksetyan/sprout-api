package httpx

import "net/http"

const docsPath = apiBasePath + "/docs"

// docsPage is Swagger UI over the served OpenAPI document. The assets are
// pinned by version and integrity hash. Being on the API's own origin, its
// "Try it out" calls need no CORS.
const docsPage = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Sprout API</title>
  <link rel="stylesheet" href="https://cdnjs.cloudflare.com/ajax/libs/swagger-ui/5.29.1/swagger-ui.min.css"
        integrity="sha384-OFyZ/BC2LThtP2XDBMEhSMeKdG9gk7udSIr1NlawKM1XL1APWlsIi856OIALfO5Q" crossorigin="anonymous">
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://cdnjs.cloudflare.com/ajax/libs/swagger-ui/5.29.1/swagger-ui-bundle.min.js"
          integrity="sha384-o1NjSPoS/7sblYrybP8KfXpC7xOOIeuc538Mb1/4/307KIQPLwqmOWvrSS5e1T5s" crossorigin="anonymous"></script>
  <script>
    window.ui = SwaggerUIBundle({
      url: "` + specPath + `",
      dom_id: "#swagger-ui",
      deepLinking: true,
      persistAuthorization: true,
      tryItOutEnabled: true
    });
  </script>
</body>
</html>
`

func handleDocs(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(docsPage))
}
