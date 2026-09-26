// Drops a non-WebSocket upgrade offer so the request is served as ordinary
// HTTP/1.1. `dummyRequest' holds this same headers object, so clearing the
// fields here is what makes the checks below see no upgrade at all.
const offered = (headers["upgrade"] ?? "").toLowerCase();
if (offered && !offered.includes("websocket")) {
    delete headers["upgrade"];
    delete headers["http2-settings"];
    const connectionTokens = (headers["connection"] ?? "")
        .split(",")
        .map((token) => token.trim())
        .filter((token) => token && !/^(upgrade|http2-settings)$/i.test(token));
    if (connectionTokens.length > 0) {
        headers["connection"] = connectionTokens.join(", ");
    }
    else {
        delete headers["connection"];
    }
}
