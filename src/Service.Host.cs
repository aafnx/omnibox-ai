using System;
using System.IO;
using System.Diagnostics;
using System.Collections.Generic;
using System.Runtime.InteropServices;
using System.Threading;
using System.Text;
using System.Text.Json;
using System.Text.RegularExpressions;
namespace OmniBox {
 public static class ServiceHost {
  [StructLayout(LayoutKind.Sequential)] struct ServiceEntry { [MarshalAs(UnmanagedType.LPWStr)] public string name; public ServiceMain main; }
  [StructLayout(LayoutKind.Sequential)] struct Status { public uint type,state,accepted,error,specific,checkpoint,hint; }
  [UnmanagedFunctionPointer(CallingConvention.Winapi)] delegate void ServiceMain(uint argc,IntPtr argv);
  [UnmanagedFunctionPointer(CallingConvention.Winapi)] delegate uint Handler(uint control,uint eventType,IntPtr data,IntPtr context);
  [DllImport("advapi32.dll",CharSet=CharSet.Unicode,SetLastError=true)] static extern bool StartServiceCtrlDispatcher(ServiceEntry[] table);
  [DllImport("advapi32.dll",CharSet=CharSet.Unicode,SetLastError=true)] static extern IntPtr RegisterServiceCtrlHandlerEx(string name,Handler handler,IntPtr context);
  [DllImport("advapi32.dll",SetLastError=true)] static extern bool SetServiceStatus(IntPtr handle,ref Status status);
  [StructLayout(LayoutKind.Sequential)] struct Limits {public long processTime,jobTime;public uint flags;public UIntPtr minWorking,maxWorking;public uint activeProcesses;public UIntPtr affinity;public uint priority,scheduling;}
  [StructLayout(LayoutKind.Sequential)] struct Io {public ulong readOps,writeOps,otherOps,readBytes,writeBytes,otherBytes;}
  [StructLayout(LayoutKind.Sequential)] struct ExtendedLimits {public Limits basic;public Io io;public UIntPtr processMemory,jobMemory,peakProcess,peakJob;}
  [DllImport("kernel32.dll",CharSet=CharSet.Unicode,SetLastError=true)] static extern IntPtr CreateJobObject(IntPtr attributes,string name);
  [DllImport("kernel32.dll",SetLastError=true)] static extern bool SetInformationJobObject(IntPtr job,int info,ref ExtendedLimits limits,uint length);
  [DllImport("kernel32.dll",SetLastError=true)] static extern bool AssignProcessToJobObject(IntPtr job,IntPtr process);
  [DllImport("kernel32.dll")] static extern bool CloseHandle(IntPtr handle);
  static IntPtr Job;
  static void ProtectProcessTree(){
    Job=CreateJobObject(IntPtr.Zero,null);if(Job==IntPtr.Zero)throw new System.ComponentModel.Win32Exception(Marshal.GetLastWin32Error());
    var limits=new ExtendedLimits();limits.basic.flags=0x2000; // JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
    if(!SetInformationJobObject(Job,9,ref limits,(uint)Marshal.SizeOf<ExtendedLimits>())||!AssignProcessToJobObject(Job,Process.GetCurrentProcess().Handle)){
      int error=Marshal.GetLastWin32Error();CloseHandle(Job);Job=IntPtr.Zero;throw new System.ComponentModel.Win32Exception(error);
    }
    // Keep the handle until this service process exits. Children inherit the job,
    // including Ollama runners; an unexpected host exit also releases them.
  }
  static readonly ManualResetEvent Stop=new ManualResetEvent(false); static readonly object Sync=new object();
  static string Name,Executable,Arguments,DirectoryPath,LogPath,Prefix,RuntimePath; static Dictionary<string,string> Env; static IntPtr Handle;
  static ServiceMain MainDelegate=Main; static Handler HandlerDelegate=Control; static Process Child;
  static DateTime Started; static int? GpuLayers,TotalLayers;
  static void Report(uint state,uint error=0){ var s=new Status{type=16,state=state,accepted=state==4?5u:0u,error=error,hint=state==2||state==3?30000u:0u,checkpoint=state==2||state==3?1u:0u}; SetServiceStatus(Handle,ref s); }
  static uint Control(uint code,uint type,IntPtr data,IntPtr context){ if(code==1||code==5){Report(3);Stop.Set();} else if(code==4)Report(Stop.WaitOne(0)?3u:4u);return 0; }
  public static void Run(string name,string executable,string arguments,string directory,string logPath,string prefix,string runtimePath,Dictionary<string,string> environment){
   if(!OperatingSystem.IsWindows())throw new PlatformNotSupportedException("Windows service runtime");
   Name=name;Executable=executable;Arguments=arguments;DirectoryPath=directory;LogPath=logPath;Prefix=prefix;RuntimePath=runtimePath;Env=environment;
   if(!StartServiceCtrlDispatcher(new[]{new ServiceEntry{name=Name,main=MainDelegate},new ServiceEntry()}))throw new System.ComponentModel.Win32Exception(Marshal.GetLastWin32Error());
  }
  static void WriteRuntime(bool running){lock(Sync){if(String.IsNullOrEmpty(RuntimePath))return;var tmp=RuntimePath+".tmp";try{File.WriteAllText(tmp,JsonSerializer.Serialize(new{started=Started.ToString("o"),running=running,pid=Child?.Id,gpu_layers=GpuLayers,total_layers=TotalLayers}),new UTF8Encoding(false));File.Move(tmp,RuntimePath,true);}catch{}}}
  static void Log(string stream,string line){if(line==null)return;lock(Sync){try{
   var m=Regex.Match(line,@"offloaded (\d+)/(\d+) layers to GPU");if(m.Success){GpuLayers=int.Parse(m.Groups[1].Value);TotalLayers=int.Parse(m.Groups[2].Value);WriteRuntime(true);}
   string p=Path.Combine(LogPath,Prefix+"."+stream+".log");if(File.Exists(p)&&new FileInfo(p).Length>4*1024*1024){if(File.Exists(p+".2"))File.Delete(p+".2");if(File.Exists(p+".1"))File.Move(p+".1",p+".2");File.Move(p,p+".1");}
   File.AppendAllText(p,DateTime.UtcNow.ToString("o")+" "+line+"\n",new UTF8Encoding(false));
  }catch{}}}
  static void Main(uint argc,IntPtr argv){
   Handle=RegisterServiceCtrlHandlerEx(Name,HandlerDelegate,IntPtr.Zero);if(Handle==IntPtr.Zero)return;Report(2);uint error=0;
   try{
    System.IO.Directory.CreateDirectory(LogPath);Started=DateTime.UtcNow;ProtectProcessTree();
    var info=new ProcessStartInfo(Executable,Arguments){WorkingDirectory=DirectoryPath,UseShellExecute=false,CreateNoWindow=true,RedirectStandardOutput=true,RedirectStandardError=true};
    foreach(var pair in Env)info.Environment[pair.Key]=pair.Value;
    Child=new Process{StartInfo=info};Child.OutputDataReceived+=(s,e)=>Log("out",e.Data);Child.ErrorDataReceived+=(s,e)=>Log("err",e.Data);
    if(!Child.Start())throw new InvalidOperationException("Child process did not start");Child.BeginOutputReadLine();Child.BeginErrorReadLine();WriteRuntime(true);Report(4);
    while(!Stop.WaitOne(250)&&!Child.HasExited){}
    if(!Child.HasExited){Report(3);Child.Kill(true);if(!Child.WaitForExit(15000))error=1053;}else if(!Stop.WaitOne(0)&&Child.ExitCode!=0)error=1066;
    Log("host","Process stopped");
   }catch(Exception e){error=1064;Log("host",e.GetType().Name+": "+e.Message);}
   finally{WriteRuntime(false);Child?.Dispose();Report(1,error);}
  }
 }
}
