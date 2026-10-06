using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using System.Threading;

namespace OmniBox {
    // This DTO is the ONLY payload written to the request journal.
    public sealed class RequestStat {
        public string id { get; set; }
        public string session_id { get; set; }
        public string backend { get; set; }
        public string model { get; set; }
        public string route { get; set; }
        public string started { get; set; }
        public string ended { get; set; }
        public string state { get; set; }
        public int status { get; set; }
        public double duration_seconds { get; set; }
        public long? input_tokens { get; set; }
        public long? output_tokens { get; set; }
        public long? total_tokens { get; set; }
        public int context_limit { get; set; }
        public long? context_used { get; set; }
        public long? context_remaining { get; set; }
        public long? prompt_processed { get; set; }
        public long? prompt_total { get; set; }
        public string usage_source { get; set; }
    }
    public sealed class SessionStat {
        public string id { get; set; }
        public string backend { get; set; }
        public string model { get; set; }
        public string started { get; set; }
        public string last_request { get; set; }
        public int requests { get; set; }
        public long input_tokens { get; set; }
        public long output_tokens { get; set; }
        public long total_tokens { get; set; }
        public int missing_usage { get; set; }
    }
    public static class InferenceStats {
        private static readonly object Sync = new object();
        private static readonly Queue<RequestStat> Recent = new Queue<RequestStat>();
        private static readonly Dictionary<string, SessionStat> Sessions = new Dictionary<string, SessionStat>();
        private static readonly Dictionary<string, string> ModelSessions = new Dictionary<string, string>();
        private static RequestStat Current;
        private static bool Control;
        private static CancellationTokenSource Cancellation;
        private static string DirectoryPath, JournalError;
        private const long FileLimit = 4 * 1024 * 1024;

        public static void Configure(string directory) {
            lock (Sync) {
                DirectoryPath = directory;
                Recent.Clear();Sessions.Clear();ModelSessions.Clear();JournalError=null;
                try {
                    Directory.CreateDirectory(directory);
                    foreach (string suffix in new[] { ".2", ".1", "" }) {
                        string path = Path.Combine(directory, "requests.jsonl" + suffix);
                        if (!File.Exists(path) || new FileInfo(path).Length > FileLimit + 16384) continue;
                        foreach (string line in File.ReadLines(path)) {
                            if (line.Length > 4096) continue;
                            try { var r = JsonSerializer.Deserialize<RequestStat>(line); if (r != null && !String.IsNullOrEmpty(r.id) && r.ended != null) Add(r); } catch { }
                        }
                    }
                } catch { JournalError = "Request journal is unavailable."; }
            }
        }
        public static bool BeginControl(bool cancelInference) {
            lock (Sync) {
                if (Control || (Current != null && !cancelInference)) return false;
                Control = true;
                if (cancelInference) Cancellation?.Cancel();
                return true;
            }
        }
        public static void EndControl() { lock (Sync) { Control = false; } }
        public static void EndModelSession(string backend) {
            lock (Sync) { foreach (string k in ModelSessions.Keys.Where(k => k.StartsWith(backend + "\n", StringComparison.Ordinal)).ToArray()) ModelSessions.Remove(k); }
        }
        public static RequestStat Begin(string backend, string model, string route, int context, string sessionHeader, CancellationTokenSource cancellation) {
            lock (Sync) {
                if (Control || Current != null) return null;
                string key = backend + "\n" + model;
                string session;
                if (!String.IsNullOrEmpty(sessionHeader) && sessionHeader.Length <= 128)
                    session = Convert.ToHexString(SHA256.HashData(Encoding.UTF8.GetBytes(key + "\n" + sessionHeader))).Substring(0, 24).ToLowerInvariant();
                else if (!ModelSessions.TryGetValue(key, out session)) ModelSessions[key] = session = Guid.NewGuid().ToString("N");
                Current = new RequestStat { id = Guid.NewGuid().ToString("N"), session_id = session, backend = backend, model = model, route = route,
                    started = DateTime.UtcNow.ToString("o"), state = "preparing", context_limit = context, usage_source = "unavailable" };
                Cancellation = cancellation;
                return Current;
            }
        }
        private static long? Number(JsonElement obj, string key) {
            if (obj.ValueKind == JsonValueKind.Object && obj.TryGetProperty(key, out var value) && value.ValueKind == JsonValueKind.Number && value.TryGetInt64(out long n) && n >= 0) return n;
            return null;
        }
        public static void Observe(RequestStat record, string json) {
            if (record == null || json.Length > 4 * 1024 * 1024) return;
            try {
                using var document = JsonDocument.Parse(json);
                var root = document.RootElement;
                if (root.ValueKind != JsonValueKind.Object) return;
                if (root.TryGetProperty("response", out var response) && response.ValueKind == JsonValueKind.Object) root = response;
                lock (Sync) {
                    if (root.TryGetProperty("usage", out var usage) && usage.ValueKind == JsonValueKind.Object) {
                        var input = Number(usage, "prompt_tokens") ?? Number(usage, "input_tokens");
                        var output = Number(usage, "completion_tokens") ?? Number(usage, "output_tokens");
                        if (input.HasValue) record.input_tokens = input;
                        if (output.HasValue) record.output_tokens = output;
                        // Embedding APIs report input and total but have no output tokens.
                        if (record.route == "/v1/embeddings" && input.HasValue) record.output_tokens = 0;
                        record.total_tokens = Number(usage, "total_tokens") ?? (record.input_tokens + record.output_tokens);
                        record.usage_source = "usage";
                    }
                    if (root.TryGetProperty("prompt_progress", out var progress)) {
                        record.prompt_total = Number(progress, "total"); record.prompt_processed = Number(progress, "processed");
                        if (record.prompt_total.HasValue) record.input_tokens = record.prompt_total;
                        record.usage_source = "engine_progress";
                    }
                    if (root.TryGetProperty("timings", out var timings)) {
                        var decoded = Number(timings, "predicted_n");
                        if (decoded.HasValue) record.output_tokens = decoded;
                    }
                    if (record.input_tokens.HasValue && record.output_tokens.HasValue) {
                        record.context_used = record.input_tokens + record.output_tokens;
                        record.context_remaining = Math.Max(0, record.context_limit - record.context_used.Value);
                        if(record.usage_source != "usage") record.total_tokens = record.context_used;
                    }
                }
            } catch (JsonException) { }
        }
        public static void Generating(RequestStat record) { lock (Sync) { if(record != null) record.state = "generating"; } }
        public static void ObserveSlot(string backend, long decoded, int context) {
            ObserveSlot(backend,decoded,context,null);
        }
        public static void ObserveSlot(string backend, long decoded, int context, string requestId) {
            lock (Sync) { if(Current != null && Current.backend == backend && (requestId==null || Current.id==requestId)) {
                Current.output_tokens = Math.Max(decoded,Current.output_tokens??0); if(context > 0) Current.context_limit = context;
                if(Current.input_tokens.HasValue){Current.context_used=Current.input_tokens+Current.output_tokens;Current.context_remaining=Math.Max(0,Current.context_limit-Current.context_used.Value);Current.total_tokens=Current.context_used;}
            } }
        }
        public static string LastActivity(string backend) {lock(Sync){return Recent.Where(r=>r.backend==backend&&r.ended!=null).OrderByDescending(r=>r.ended).FirstOrDefault()?.ended;}}
        public static bool IsActive(string backend) { lock(Sync){return Current != null && Current.backend==backend;} }
        public static string ActiveId(string backend) { lock(Sync){return Current != null && Current.backend==backend?Current.id:null;} }
        private static void Add(RequestStat record) {
            Recent.Enqueue(record); while (Recent.Count > 500) Recent.Dequeue();
            if (!Sessions.TryGetValue(record.session_id, out var session)) {
                if(Sessions.Count >= 100) Sessions.Remove(Sessions.Values.OrderBy(s => s.last_request).First().id);
                Sessions[record.session_id] = session = new SessionStat { id=record.session_id, backend=record.backend, model=record.model, started=record.started };
            }
            session.requests++; session.last_request=record.ended;
            session.input_tokens += record.input_tokens ?? 0; session.output_tokens += record.output_tokens ?? 0; session.total_tokens += record.total_tokens ?? 0;
            if(!record.input_tokens.HasValue || !record.output_tokens.HasValue) session.missing_usage++;
        }
        public static void Finish(RequestStat record, int status, bool completed) {
            if (record == null) return;
            lock (Sync) {
                record.ended = DateTime.UtcNow.ToString("o"); record.status = status;
                record.state = completed && status < 400 ? "completed" : "failed";
                record.duration_seconds = Math.Round((DateTime.UtcNow - DateTime.Parse(record.started).ToUniversalTime()).TotalSeconds, 3);
                Add(record);
                try {
                    string path=Path.Combine(DirectoryPath,"requests.jsonl");
                    if(File.Exists(path) && new FileInfo(path).Length >= FileLimit) {
                        if(File.Exists(path+".2")) File.Delete(path+".2");
                        if(File.Exists(path+".1")) File.Move(path+".1",path+".2");
                        File.Move(path,path+".1");
                    }
                    File.AppendAllText(path, JsonSerializer.Serialize(record)+"\n", new UTF8Encoding(false));
                } catch { JournalError = "Request journal could not be written."; }
                if (ReferenceEquals(Current,record)) { Current=null; Cancellation=null; }
            }
        }
        public static string Snapshot() {
            lock (Sync) {
                return JsonSerializer.Serialize(new { active=Current, control_busy=Control, requests=Recent.Reverse().Take(200).ToArray(),
                    sessions=Sessions.Values.OrderByDescending(s=>s.last_request).ToArray(), journal_error=JournalError });
            }
        }
    }
}
