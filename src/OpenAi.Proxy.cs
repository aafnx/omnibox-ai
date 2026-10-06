using System;
using System.IO;
using System.Net;
using System.Net.Http;
using System.Net.Http.Headers;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using System.Text.Json.Nodes;
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
            var bytes = Encoding.UTF8.GetBytes(JsonSerializer.Serialize(new {error=new {message,type="omnibox_error",code}}));
            ctx.Response.StatusCode = code; ctx.Response.ContentType = "application/json; charset=utf-8";
            ctx.Response.Headers["Cache-Control"] = "no-store"; ctx.Response.SendChunked = false;
            ctx.Response.ContentLength64 = bytes.Length;
            await ctx.Response.OutputStream.WriteAsync(bytes, 0, bytes.Length);
        }
        private sealed class UsageReader {
            private readonly RequestStat Record;
            private readonly bool SSE;
            private readonly Decoder Decoder = Encoding.UTF8.GetDecoder();
            private readonly StringBuilder Text = new StringBuilder();
            private readonly StringBuilder Event = new StringBuilder();
            private bool Overflow;
            public bool Done;
            public UsageReader(RequestStat record, bool sse) { Record=record; SSE=sse; }
            public void Feed(byte[] bytes, int count) {
                var chars=new char[count+4]; int n=Decoder.GetChars(bytes,0,count,chars,0,false);
                if(!SSE) { if(!Overflow && Text.Length+n<=4*1024*1024) Text.Append(chars,0,n); else { Overflow=true; Text.Clear(); } return; }
                for(int i=0;i<n;i++) {
                    if(chars[i]!='\n') { if(Text.Length<1024*1024)Text.Append(chars[i]);else Overflow=true;continue; }
                    string line=Text.ToString().TrimEnd('\r'); Text.Clear();
                    if(line.Length==0) {
                        if(!Overflow && Event.Length>0) {
                            string data=Event.ToString();
                            if(data=="[DONE]")Done=true;
                            else { InferenceStats.Observe(Record,data); if(data.Contains("\"response.completed\""))Done=true; }
                        }
                        Event.Clear(); Overflow=false;
                    } else if(line.StartsWith("data:",StringComparison.Ordinal) && !Overflow) {
                        if(Event.Length>0)Event.Append('\n'); Event.Append(line.Substring(5).TrimStart());
                        if(Event.Length>4*1024*1024){Event.Clear();Overflow=true;}
                    }
                }
            }
            public void Finish() { if(!SSE && !Overflow)InferenceStats.Observe(Record,Text.ToString()); }
        }
        public static Task Handle(HttpListenerContext ctx, string baseUrl, bool requireKey, string hash, string upstreamKey,
            string backend, string selectedModel, int contextLimit, string engineType, string placement = "auto") {
            return Task.Run(async () => {
                using var timeout = new CancellationTokenSource(TimeSpan.FromMinutes(30));
                bool acquired=false, started=false, completed=false;
                int status=502; RequestStat record=null;
                try {
                    var req=ctx.Request; var path=req.Url.AbsolutePath;
                    bool allowed=(req.HttpMethod=="GET" && path=="/v1/models") ||
                        (req.HttpMethod=="POST" && (path=="/v1/chat/completions" || path=="/v1/completions" || path=="/v1/embeddings" || path=="/v1/responses"));
                    if(!allowed || req.Url.Query.Length!=0){await Error(ctx,404,"Unsupported OpenAI route.");return;}
                    if(requireKey) {
                        var auth=req.Headers["Authorization"]??"";
                        if(!auth.StartsWith("Bearer ",StringComparison.OrdinalIgnoreCase)||auth.Length>256||hash.Length!=64){await Error(ctx,401,"API key required.");return;}
                        var actual=SHA256.HashData(Encoding.UTF8.GetBytes(auth.Substring(7)));
                        if(!CryptographicOperations.FixedTimeEquals(actual,Convert.FromHexString(hash))){await Error(ctx,401,"Invalid API key.");return;}
                    }
                    if(Interlocked.Increment(ref Active)>8){Interlocked.Decrement(ref Active);await Error(ctx,429,"Too many simultaneous requests.");return;}
                    acquired=true;
                    var target=new Uri(baseUrl.TrimEnd('/')+path);
                    if(target.Scheme!="http"||!target.IsLoopback||target.UserInfo.Length!=0){await Error(ctx,503,"Invalid backend configuration.");return;}
                    using(var forward=new HttpRequestMessage(new HttpMethod(req.HttpMethod),target)) {
                        if(req.HttpMethod=="POST") {
                            if(req.ContentLength64>BodyLimit){await Error(ctx,413,"Request exceeds 8 MiB.");return;}
                            if(!(req.ContentType??"").StartsWith("application/json",StringComparison.OrdinalIgnoreCase)){await Error(ctx,415,"JSON content required.");return;}
                            byte[] body;
                            using(var memory=new MemoryStream()) {
                                var b=new byte[8192];int n;
                                while((n=await req.InputStream.ReadAsync(b,0,b.Length,timeout.Token))>0){if(memory.Length+n>BodyLimit){await Error(ctx,413,"Request exceeds 8 MiB.");return;}memory.Write(b,0,n);}
                                body=memory.ToArray();
                            }
                            JsonObject json;
                            try{json=JsonNode.Parse(body) as JsonObject;if(json==null)throw new JsonException();}
                            catch(JsonException){await Error(ctx,400,"A JSON object is required.");return;}
                            string model;
                            try{model=json["model"]?.GetValue<string>();}catch{await Error(ctx,400,"A model name is required.");return;}
                            bool latestAlias=!String.IsNullOrEmpty(selectedModel)&&selectedModel.EndsWith(":latest",StringComparison.Ordinal)&&model==selectedModel.Substring(0,selectedModel.Length-7);
                            if(String.IsNullOrEmpty(selectedModel)||!(model==selectedModel||latestAlias)){await Error(ctx,409,"Load/select the model in OmniBox AI first. Only that model is available.");return;}
                            foreach(string field in new[]{"n","best_of"})if(json[field]!=null && (!(json[field] is JsonValue v)||!v.TryGetValue<int>(out int one)||one!=1)){await Error(ctx,400,"Only one output sequence is allowed.");return;}
                            if(json["options"] is JsonObject overrides && overrides["num_ctx"]!=null){
                                if(!(overrides["num_ctx"] is JsonValue cv)||!cv.TryGetValue<int>(out int requestedContext)||requestedContext!=contextLimit){await Error(ctx,400,"Change model context in OmniBox AI before sending requests.");return;}
                            }
                            if(engineType=="ollama") {
                                if(json["options"]!=null && !(json["options"] is JsonObject)){await Error(ctx,400,"options must be an object.");return;}
                                var loadOptions=json["options"] as JsonObject ?? new JsonObject();
                                if(loadOptions["num_gpu"]!=null){await Error(ctx,400,"Change model placement in OmniBox AI.");return;}
                                loadOptions["num_ctx"]=contextLimit;
                                if(placement=="cpu")loadOptions["num_gpu"]=0;
                                json["options"]=loadOptions;
                                body=Encoding.UTF8.GetBytes(json.ToJsonString());
                            }
                            record=InferenceStats.Begin(backend,selectedModel,path,contextLimit,req.Headers["X-Omnibox-Session"],timeout);
                            if(record==null){await Error(ctx,409,"The model is busy or a control operation is running. Retry after completion.");return;}
                            bool streaming=json["stream"] is JsonValue sv && sv.TryGetValue<bool>(out bool flag) && flag;
                            if(streaming && (path=="/v1/chat/completions"||path=="/v1/completions")) {
                                var options=json["stream_options"] as JsonObject;
                                if(options==null){options=new JsonObject();json["stream_options"]=options;}options["include_usage"]=true;
                                if(engineType=="prism"){json["timings_per_token"]=true;json["return_progress"]=true;}
                                body=Encoding.UTF8.GetBytes(json.ToJsonString());
                            }
                            forward.Content=new ByteArrayContent(body);forward.Content.Headers.ContentType=new MediaTypeHeaderValue("application/json");
                        }
                        if(!String.IsNullOrEmpty(upstreamKey))forward.Headers.Authorization=new AuthenticationHeaderValue("Bearer",upstreamKey);
                        using(var response=await Client.SendAsync(forward,HttpCompletionOption.ResponseHeadersRead,timeout.Token)) {
                            status=(int)response.StatusCode;
                            if(req.HttpMethod=="GET" && response.IsSuccessStatusCode) {
                                using var list=JsonDocument.Parse(await response.Content.ReadAsStringAsync(timeout.Token));
                                var models=new System.Collections.Generic.List<JsonElement>();
                                if(list.RootElement.TryGetProperty("data",out var data))foreach(var item in data.EnumerateArray())if(item.TryGetProperty("id",out var id)&&id.GetString()==selectedModel)models.Add(item.Clone());
                                var bytes=JsonSerializer.SerializeToUtf8Bytes(new { @object="list",data=models });
                                ctx.Response.StatusCode=200;ctx.Response.ContentType="application/json";ctx.Response.Headers["Cache-Control"]="no-store";ctx.Response.ContentLength64=bytes.Length;
                                await ctx.Response.OutputStream.WriteAsync(bytes,0,bytes.Length,timeout.Token);started=true;completed=true;return;
                            }
                            ctx.Response.StatusCode=status;ctx.Response.ContentType=response.Content.Headers.ContentType?.ToString()??"application/json";
                            ctx.Response.Headers["Cache-Control"]="no-store";ctx.Response.Headers["X-Accel-Buffering"]="no";ctx.Response.Headers["X-Content-Type-Options"]="nosniff";ctx.Response.SendChunked=true;
                            bool sse=ctx.Response.ContentType.StartsWith("text/event-stream",StringComparison.OrdinalIgnoreCase);
                            var observer=new UsageReader(record,sse);
                            using(var stream=await response.Content.ReadAsStreamAsync(timeout.Token)) {
                                var buffer=new byte[8192];int n;
                                while((n=await stream.ReadAsync(buffer,0,buffer.Length,timeout.Token))>0) {
                                    started=true;InferenceStats.Generating(record);observer.Feed(buffer,n);
                                    await ctx.Response.OutputStream.WriteAsync(buffer,0,n,timeout.Token);await ctx.Response.OutputStream.FlushAsync(timeout.Token);
                                }
                            }
                            observer.Finish();completed=!sse||observer.Done;
                        }
                    }
                }catch(HttpRequestException){status=503;if(!started){try{await Error(ctx,503,"Backend unavailable. Start the selected engine in OmniBox AI.");}catch{}}}
                catch(OperationCanceledException){status=504;if(!started){try{await Error(ctx,504,"Backend request timed out or was stopped.");}catch{}}}
                catch{status=502;if(!started){try{await Error(ctx,502,"Proxy request failed.");}catch{}}}
                finally{if(acquired)Interlocked.Decrement(ref Active);InferenceStats.Finish(record,status,completed);try{ctx.Response.Close();}catch{}}
            });
        }
    }
}
