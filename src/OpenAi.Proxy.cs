using System;
using System.IO;
using System.Net;
using System.Net.Http;
using System.Net.Http.Headers;
using System.Security.Cryptography;
using System.Text;
using System.Threading;
using System.Threading.Tasks;

namespace OmniBox {
    public static class OpenAiProxy {
        private static readonly HttpClient Client = new HttpClient(new HttpClientHandler {
            AllowAutoRedirect = false, UseProxy = false, MaxConnectionsPerServer = 8
        }) { Timeout = TimeSpan.FromMinutes(30) };
        private static int Active;
        private const int BodyLimit = 8 * 1024 * 1024;

        private static async Task Error(HttpListenerContext ctx, int code, string message) {
            var bytes = Encoding.UTF8.GetBytes("{\"error\":{\"message\":\"" + message + "\",\"type\":\"omnibox_error\",\"code\":" + code + "}}");
            ctx.Response.StatusCode = code;
            ctx.Response.ContentType = "application/json; charset=utf-8";
            ctx.Response.Headers["Cache-Control"] = "no-store";
            ctx.Response.SendChunked = false;
            ctx.Response.ContentLength64 = bytes.Length;
            await ctx.Response.OutputStream.WriteAsync(bytes, 0, bytes.Length);
        }

        public static Task Handle(HttpListenerContext ctx, string baseUrl, bool requireKey, string hash, string upstreamKey) {
            // No PowerShell runspace is held during inference or SSE transmission.
            return Task.Run(async () => {
                using var timeout = new CancellationTokenSource(TimeSpan.FromMinutes(30));
                bool acquired = false, started = false;
                try {
                    var req = ctx.Request;
                    var path = req.Url.AbsolutePath;
                    bool allowed = (req.HttpMethod == "GET" && path == "/v1/models") ||
                        (req.HttpMethod == "POST" && (path == "/v1/chat/completions" || path == "/v1/completions" || path == "/v1/embeddings" || path == "/v1/responses"));
                    if (!allowed || req.Url.Query.Length != 0) { await Error(ctx, 404, "Unsupported OpenAI route."); return; }
                    if (requireKey) {
                        var auth = req.Headers["Authorization"] ?? "";
                        if (!auth.StartsWith("Bearer ", StringComparison.OrdinalIgnoreCase) || auth.Length > 256 || hash.Length != 64) { await Error(ctx, 401, "API key required."); return; }
                        var actual = SHA256.HashData(Encoding.UTF8.GetBytes(auth.Substring(7)));
                        if (!CryptographicOperations.FixedTimeEquals(actual, Convert.FromHexString(hash))) { await Error(ctx, 401, "Invalid API key."); return; }
                    }
                    if (Interlocked.Increment(ref Active) > 8) { Interlocked.Decrement(ref Active); await Error(ctx, 429, "Too many simultaneous requests."); return; }
                    acquired = true;
                    var target = new Uri(baseUrl.TrimEnd('/') + path);
                    if (target.Scheme != "http" || !target.IsLoopback || target.UserInfo.Length != 0) { await Error(ctx, 503, "Invalid backend configuration."); return; }
                    using (var forward = new HttpRequestMessage(new HttpMethod(req.HttpMethod), target)) {
                        if (req.HttpMethod == "POST") {
                            if (req.ContentLength64 > BodyLimit) { await Error(ctx, 413, "Request exceeds 8 MiB."); return; }
                            if (!(req.ContentType ?? "").StartsWith("application/json", StringComparison.OrdinalIgnoreCase)) { await Error(ctx, 415, "JSON content required."); return; }
                            using (var memory = new MemoryStream()) {
                                var buffer = new byte[8192]; int n;
                                while ((n = await req.InputStream.ReadAsync(buffer, 0, buffer.Length, timeout.Token)) > 0) {
                                    if (memory.Length + n > BodyLimit) { await Error(ctx, 413, "Request exceeds 8 MiB."); return; }
                                    memory.Write(buffer, 0, n);
                                }
                                forward.Content = new ByteArrayContent(memory.ToArray());
                            }
                            forward.Content.Headers.ContentType = new MediaTypeHeaderValue("application/json");
                        }
                        // Never pass the gateway key/cookies to the engine.
                        if (!String.IsNullOrEmpty(upstreamKey)) forward.Headers.Authorization = new AuthenticationHeaderValue("Bearer", upstreamKey);
                        using (var response = await Client.SendAsync(forward, HttpCompletionOption.ResponseHeadersRead, timeout.Token)) {
                            ctx.Response.StatusCode = (int)response.StatusCode;
                            ctx.Response.ContentType = response.Content.Headers.ContentType?.ToString() ?? "application/json";
                            ctx.Response.Headers["Cache-Control"] = "no-store";
                            ctx.Response.Headers["X-Accel-Buffering"] = "no";
                            ctx.Response.Headers["X-Content-Type-Options"] = "nosniff";
                            ctx.Response.SendChunked = true;
                            using (var stream = await response.Content.ReadAsStreamAsync()) {
                                var buffer = new byte[8192]; int n;
                                while ((n = await stream.ReadAsync(buffer, 0, buffer.Length, timeout.Token)) > 0) {
                                    started = true;
                                    await ctx.Response.OutputStream.WriteAsync(buffer, 0, n, timeout.Token);
                                    await ctx.Response.OutputStream.FlushAsync(timeout.Token);
                                }
                            }
                        }
                    }
                } catch (HttpRequestException) {
                    if (!started) { try { await Error(ctx, 503, "Backend unavailable. Start the selected engine in OmniBox AI."); } catch { } }
                } catch (TaskCanceledException) {
                    if (!started) { try { await Error(ctx, 504, "Backend request timed out."); } catch { } }
                } catch {
                    if (!started) { try { await Error(ctx, 502, "Proxy request failed."); } catch { } }
                } finally {
                    if (acquired) Interlocked.Decrement(ref Active);
                    try { ctx.Response.Close(); } catch { }
                }
            });
        }
    }
}
