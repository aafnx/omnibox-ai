// C# 5 / .NET Framework 4.x: compiled by the compiler shipped with Windows.
// The service executable connects to SCM before XML, PowerShell or model initialization.
using System;
using System.IO;
using System.Diagnostics;
using System.Collections.Generic;
using System.Runtime.InteropServices;
using System.Threading;
using System.Text;
using System.Xml;
using System.Text.RegularExpressions;
namespace OmniBox {
 public sealed class LaunchProfile {
  public string Id,Executable,Arguments,DirectoryPath,LogPath;
  public Dictionary<string,string> Environment=new Dictionary<string,string>();
 }
 public static class ServiceHost {
  [StructLayout(LayoutKind.Sequential)] struct ServiceEntry { [MarshalAs(UnmanagedType.LPWStr)] public string name; public IntPtr main; }
  [StructLayout(LayoutKind.Sequential)] struct Status { public uint type,state,accepted,error,specific,checkpoint,hint; }
  [UnmanagedFunctionPointer(CallingConvention.Winapi)] delegate void ServiceMain(uint argc,IntPtr argv);
  [UnmanagedFunctionPointer(CallingConvention.Winapi)] delegate uint Handler(uint control,uint eventType,IntPtr data,IntPtr context);
  [DllImport("advapi32.dll",CharSet=CharSet.Unicode,SetLastError=true)] static extern bool StartServiceCtrlDispatcher([In] ServiceEntry[] table);
  [DllImport("advapi32.dll",CharSet=CharSet.Unicode,SetLastError=true)] static extern IntPtr RegisterServiceCtrlHandlerEx(string name,Handler handler,IntPtr context);
  [DllImport("advapi32.dll",SetLastError=true)] static extern bool SetServiceStatus(IntPtr handle,ref Status status);
  [StructLayout(LayoutKind.Sequential)] struct Limits {public long processTime,jobTime;public uint flags;public UIntPtr minWorking,maxWorking;public uint activeProcesses;public UIntPtr affinity;public uint priority,scheduling;}
  [StructLayout(LayoutKind.Sequential)] struct Io {public ulong readOps,writeOps,otherOps,readBytes,writeBytes,otherBytes;}
  [StructLayout(LayoutKind.Sequential)] struct ExtendedLimits {public Limits basic;public Io io;public UIntPtr processMemory,jobMemory,peakProcess,peakJob;}
  [DllImport("kernel32.dll",CharSet=CharSet.Unicode,SetLastError=true)] static extern IntPtr CreateJobObject(IntPtr attributes,string name);
  [DllImport("kernel32.dll",SetLastError=true)] static extern bool SetInformationJobObject(IntPtr job,int info,ref ExtendedLimits limits,uint length);
  [DllImport("kernel32.dll",SetLastError=true)] static extern bool AssignProcessToJobObject(IntPtr job,IntPtr process);
  [DllImport("kernel32.dll")] static extern bool CloseHandle(IntPtr handle);
  static IntPtr Job,Handle;
  static readonly ManualResetEvent Stop=new ManualResetEvent(false);
  static readonly object Sync=new object();
  static readonly ServiceMain MainDelegate=OnServiceMain;
  static readonly Handler HandlerDelegate=Control;
  static string Name,ProfilePath,LogPath,Prefix,RuntimePath;
  static Process Child;
  static int? ChildId,GpuLayers,TotalLayers;
  static DateTime Started;
  static uint ExitError;
  static volatile bool Running;
  static int Main(string[] args){
   try{
    if(Environment.OSVersion.Platform!=PlatformID.Win32NT)throw new PlatformNotSupportedException("Windows service runtime");
    if(args.Length!=2 || !String.Equals(args[0],"-Profile",StringComparison.OrdinalIgnoreCase))throw new ArgumentException("Usage: Service.Host.exe -Profile <path>");
    ProfilePath=Path.GetFullPath(args[1]);RuntimePath=ProfilePath+".runtime.json";
    Prefix=Path.GetFileNameWithoutExtension(ProfilePath);LogPath=Path.Combine(Path.GetDirectoryName(ProfilePath),"logs");
    // In an OWN_PROCESS service, SCM ignores the table name and supplies the actual name in argv[0].
    var table=new[]{new ServiceEntry{name="omnibox-ai",main=Marshal.GetFunctionPointerForDelegate(MainDelegate)},new ServiceEntry()};
    if(!StartServiceCtrlDispatcher(table))throw new System.ComponentModel.Win32Exception(Marshal.GetLastWin32Error());
    GC.KeepAlive(MainDelegate);GC.KeepAlive(HandlerDelegate);
    return (int)ExitError;
   }catch(Exception e){Log("bootstrap",e.ToString());return 1;}
  }
  static string Expand(string value,string directory){return Environment.ExpandEnvironmentVariables((value??"").Replace("%BASE%",directory));}
  public static LaunchProfile ReadProfile(string path){
   var settings=new XmlReaderSettings{DtdProcessing=DtdProcessing.Prohibit,XmlResolver=null};
   var xml=new XmlDocument{XmlResolver=null};using(var reader=XmlReader.Create(path,settings)){xml.Load(reader);}
   string directory=Path.GetDirectoryName(Path.GetFullPath(path));
   var p=new LaunchProfile();
   p.Id=Text(xml,"/service/id");p.Executable=Expand(Text(xml,"/service/executable"),directory);
   p.Arguments=Expand(Text(xml,"/service/arguments"),directory);p.DirectoryPath=Expand(Text(xml,"/service/workingdirectory"),directory);
   p.LogPath=Expand(Text(xml,"/service/logpath"),directory);
   if(String.IsNullOrWhiteSpace(p.Id)||String.IsNullOrWhiteSpace(p.Executable)||String.IsNullOrWhiteSpace(p.DirectoryPath)||String.IsNullOrWhiteSpace(p.LogPath))throw new InvalidDataException("Incomplete service profile");
   foreach(XmlElement e in xml.SelectNodes("/service/env")){string key=e.GetAttribute("name");if(String.IsNullOrWhiteSpace(key))throw new InvalidDataException("Empty environment name");p.Environment[key]=Expand(e.GetAttribute("value"),directory);}
   return p;
  }
  static string Text(XmlDocument xml,string xpath){var n=xml.SelectSingleNode(xpath);return n==null?"":n.InnerText;}
  static void ProtectProcessTree(){
   Job=CreateJobObject(IntPtr.Zero,null);if(Job==IntPtr.Zero)throw new System.ComponentModel.Win32Exception(Marshal.GetLastWin32Error());
   var limits=new ExtendedLimits();limits.basic.flags=0x2000;
   if(!SetInformationJobObject(Job,9,ref limits,(uint)Marshal.SizeOf(typeof(ExtendedLimits)))||!AssignProcessToJobObject(Job,Process.GetCurrentProcess().Handle)){
    int error=Marshal.GetLastWin32Error();CloseHandle(Job);Job=IntPtr.Zero;throw new System.ComponentModel.Win32Exception(error);
   }
   // Keep this unnamed job open for the lifetime of the host: all descendants inherit it.
  }
  static void Report(uint state,uint error){var s=new Status{type=16,state=state,accepted=state==4?5u:0u,error=error,hint=state==2||state==3?30000u:0u,checkpoint=state==2||state==3?1u:0u};if(!SetServiceStatus(Handle,ref s))Log("host","SetServiceStatus failed: "+Marshal.GetLastWin32Error());}
  static uint Control(uint code,uint eventType,IntPtr data,IntPtr context){if(code==1||code==5){Report(3,0);Stop.Set();}else if(code==4)Report(Stop.WaitOne(0)?3u:4u,0);return 0;}
  static string Number(int? value){return value.HasValue?value.Value.ToString(System.Globalization.CultureInfo.InvariantCulture):"null";}
  static void WriteRuntime(bool running){lock(Sync){if(String.IsNullOrEmpty(RuntimePath))return;string tmp=RuntimePath+".tmp";try{
   string json="{\"started\":\""+Started.ToString("o")+"\",\"running\":"+(running?"true":"false")+",\"pid\":"+Number(ChildId)+",\"gpu_layers\":"+Number(GpuLayers)+",\"total_layers\":"+Number(TotalLayers)+"}";
   File.WriteAllText(tmp,json,new UTF8Encoding(false));if(File.Exists(RuntimePath))File.Replace(tmp,RuntimePath,null);else File.Move(tmp,RuntimePath);
  }catch(Exception e){Log("host","Runtime state write failed: "+e.Message);}}}
  static void Log(string stream,string line){if(line==null||String.IsNullOrEmpty(LogPath))return;lock(Sync){try{
   Directory.CreateDirectory(LogPath);
   var m=Regex.Match(line,@"offloaded (\d+)/(\d+) layers to GPU");if(m.Success){GpuLayers=int.Parse(m.Groups[1].Value);TotalLayers=int.Parse(m.Groups[2].Value);WriteRuntime(Running);}
   string p=Path.Combine(LogPath,Prefix+"."+stream+".log");if(File.Exists(p)&&new FileInfo(p).Length>4*1024*1024){if(File.Exists(p+".2"))File.Delete(p+".2");if(File.Exists(p+".1"))File.Move(p+".1",p+".2");File.Move(p,p+".1");}
   File.AppendAllText(p,DateTime.UtcNow.ToString("o")+" "+line+"\n",new UTF8Encoding(false));
  }catch{}}}
  static void OnServiceMain(uint argc,IntPtr argv){
   try{
    if(argc==0||argv==IntPtr.Zero)throw new InvalidOperationException("SCM did not supply service name");
    Name=Marshal.PtrToStringUni(Marshal.ReadIntPtr(argv));
    Handle=RegisterServiceCtrlHandlerEx(Name,HandlerDelegate,IntPtr.Zero);
    if(Handle==IntPtr.Zero)throw new System.ComponentModel.Win32Exception(Marshal.GetLastWin32Error());
    Report(2,0);Log("bootstrap","Connected to SCM: "+Name);
    var p=ReadProfile(ProfilePath);if(!String.Equals(p.Id,Name,StringComparison.OrdinalIgnoreCase))throw new InvalidDataException("Service ID differs from SCM name");
    LogPath=p.LogPath;Directory.CreateDirectory(LogPath);Started=DateTime.UtcNow;ProtectProcessTree();
    var info=new ProcessStartInfo(p.Executable,p.Arguments){WorkingDirectory=p.DirectoryPath,UseShellExecute=false,CreateNoWindow=true,RedirectStandardOutput=true,RedirectStandardError=true,StandardOutputEncoding=Encoding.UTF8,StandardErrorEncoding=Encoding.UTF8};
    foreach(var pair in p.Environment)info.EnvironmentVariables[pair.Key]=pair.Value;
    Child=new Process{StartInfo=info};Child.OutputDataReceived+=(s,e)=>Log("out",e.Data);Child.ErrorDataReceived+=(s,e)=>Log("err",e.Data);
    if(!Child.Start())throw new InvalidOperationException("Child process did not start");ChildId=Child.Id;
    Running=true;Child.BeginOutputReadLine();Child.BeginErrorReadLine();WriteRuntime(true);Report(4,0);
    while(!Stop.WaitOne(250)&&!Child.HasExited){}
    if(!Child.HasExited){Report(3,0);Child.Kill();if(!Child.WaitForExit(15000))ExitError=1053;}else if(!Stop.WaitOne(0)&&Child.ExitCode!=0)ExitError=1066;
    Log("host","Process stopped");
   }catch(Exception e){ExitError=1064;Log("host",e.ToString());}
   finally{Running=false;WriteRuntime(false);if(Child!=null)Child.Dispose();if(Handle!=IntPtr.Zero)Report(1,ExitError);}
  }
 }
}
