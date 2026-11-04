; Mailsense per-user installer (Inno Setup 6+).
;
; Build: scripts/build.sh windows, then
;   iscc packaging\windows\mailsense.iss
; The version and the payload path come from dist/VERSION and dist/, which the
; build script owns; there is no second version to keep in sync.
;
; The install needs no administrator rights. The application goes under the
; user's LocalAppData and keeps its database in %LOCALAPPDATA%\Mailsense across
; upgrades and uninstalls.

#define RepoRoot AddBackslash(AddBackslash(SourcePath) + "..\..")
#define VersionFile RepoRoot + "dist\VERSION"
#define DefaultSourceExe RepoRoot + "dist\mailsense.exe"
#define LicenseFile RepoRoot + "LICENSE"
#define NoticeFile RepoRoot + "NOTICE"
#define PagesInclude RepoRoot + "dist\docs-pages.iss"

; ---- one version owner -----------------------------------------------------
; scripts/build.sh writes dist/VERSION. The installer reads that file, so the
; binary stamp, the installer name, the tarball name and the About surface
; cannot disagree. /DAppVersion is still accepted from older build commands,
; but it must match the file or the compile stops.
#if !FileExists(VersionFile)
  #error dist\VERSION is missing: run scripts/build.sh windows before compiling the installer
#endif
#define VersionHandle FileOpen(VersionFile)
#if !VersionHandle
  #error cannot open dist\VERSION
#endif
#define DistVersion Trim(FileRead(VersionHandle))
#expr FileClose(VersionHandle)
#undef VersionHandle
#if DistVersion == ""
  #error dist\VERSION is empty: run scripts/build.sh windows before compiling the installer
#endif
#ifdef AppVersion
  #if !SameStr(AppVersion, DistVersion)
    #error /DAppVersion does not match dist\VERSION; scripts/build.sh owns the version
  #endif
#else
  #define AppVersion DistVersion
#endif

; ---- payload guards --------------------------------------------------------
#ifndef SourceExe
  #define SourceExe DefaultSourceExe
#endif
#if !FileExists(SourceExe)
  #error the payload binary is missing: build it with scripts/build.sh windows
#endif
#if FileSize(SourceExe) <= 0
  #error the payload binary is empty: build it with scripts/build.sh windows
#endif
#if !FileExists(LicenseFile)
  #error LICENSE is missing from the repository root
#endif
#if FileSize(LicenseFile) <= 0
  #error LICENSE is empty
#endif
#if !FileExists(NoticeFile)
  #error NOTICE is missing from the repository root
#endif
#if FileSize(NoticeFile) <= 0
  #error NOTICE is empty
#endif
#if !FileExists(RepoRoot + "cmd\mailsense\mailsense.ico")
  #error the application icon is missing: cmd\mailsense\mailsense.ico
#endif

; ---- public docs list ------------------------------------------------------
; The page list has one owner, internal/site, read by
; `go run ./cmd/gensite -pages=en|fr`. These Exec calls write one generated
; [Files] include, so neither installer section nor the Linux tarball carries a
; hand-written page list. The include is build output under dist/ and is never
; committed.
#define GenerateEn Exec('cmd.exe', '/c del /q "' + PagesInclude + '" 2>nul & for /f "usebackq delims=" %p in (`go run ./cmd/gensite -pages=en`) do @echo Source: "..\..\%p"; DestDir: "{app}\docs"; Flags: ignoreversion >> "' + PagesInclude + '"', RepoRoot)
#if GenerateEn != 0
  #error cannot generate the public English docs list
#endif
#if FileSize(PagesInclude) <= 0
  #error the generated public English docs list is empty
#endif
#define GenerateFr Exec('cmd.exe', '/c for /f "usebackq delims=" %p in (`go run ./cmd/gensite -pages=fr`) do @echo Source: "..\..\%p"; DestDir: "{app}\docs\fr"; Flags: ignoreversion >> "' + PagesInclude + '"', RepoRoot)
#if GenerateFr != 0
  #error cannot generate the public French docs list
#endif
#if FileSize(PagesInclude) <= 0
  #error the generated public docs list is empty
#endif

[Setup]
AppId={{6E2E7E2B-7C1F-4C55-9E4B-2F7A6C1D9A31}
AppName=Mailsense
AppVersion={#AppVersion}
VersionInfoProductTextVersion={#AppVersion}
AppPublisher=Mailsense
AppPublisherURL=https://github.com/LLM-4-People/mailsense
AppSupportURL=https://github.com/LLM-4-People/mailsense/issues
AppUpdatesURL=https://github.com/LLM-4-People/mailsense/releases
DefaultDirName={localappdata}\Programs\Mailsense
DefaultGroupName=Mailsense
DisableProgramGroupPage=yes
PrivilegesRequired=lowest
OutputDir=..\..\dist
OutputBaseFilename=mailsense-{#AppVersion}-setup
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
SetupIconFile=..\..\cmd\mailsense\mailsense.ico
UninstallDisplayName=Mailsense {#AppVersion}
UninstallDisplayIcon={app}\mailsense.exe
CloseApplications=yes
RestartApplications=no

; The owner downloads a small setup. The model and runtime are fetched later,
; and the first-run setup explains the choice.
[Messages]
WelcomeLabel2=This installs Mailsense for your Windows account and needs no administrator rights.%n%nThe download is small. The local model and runtime (about 342 MB) are downloaded once on first use, so the first launch can take a few minutes. Your mail and settings stay in your local app data folder under Mailsense and are kept when you uninstall.
FinishedLabel=Mailsense is installed. The first-run setup opens the first time you launch it. Your mail and settings stay in your local app data folder under Mailsense.

[Files]
Source: "{#SourceExe}"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#LicenseFile}"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#NoticeFile}"; DestDir: "{app}"; Flags: ignoreversion
; Generated from internal/site by the Exec calls above. Every public page in
; both languages ships, the same set the Linux tarball copies.
#include PagesInclude

[Icons]
Name: "{group}\Mailsense"; Filename: "{app}\mailsense.exe"; WorkingDir: "{app}"; Comment: "Local-first mail triage that explains every decision"
Name: "{autodesktop}\Mailsense"; Filename: "{app}\mailsense.exe"; WorkingDir: "{app}"; Tasks: desktopicon

[Tasks]
Name: "desktopicon"; Description: "{cm:CreateDesktopIcon}"; GroupDescription: "{cm:AdditionalIcons}"; Flags: unchecked

[Run]
; The checkbox is shown after a normal install. Mailsense opens its first-run
; setup itself when no setup completion marker exists, so a first launch goes
; straight into the sequence.
Filename: "{app}\mailsense.exe"; WorkingDir: "{app}"; Description: "{cm:LaunchProgram,Mailsense}"; Flags: nowait postinstall skipifsilent

; No [UninstallDelete]: Inno removes exactly the files it installed. The data
; directory (%LOCALAPPDATA%\Mailsense) is outside {app} and is never deleted by
; this installer or its uninstaller.

[Code]
function WebView2Missing(): Boolean;
var
  Key: String;
begin
  { The Evergreen runtime registers its version under this key. }
  Result := not RegQueryStringValue(HKLM, 'SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}', 'pv', Key)
    and not RegQueryStringValue(HKCU, 'Software\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}', 'pv', Key);
end;

procedure CurStepChanged(CurStep: TSetupStep);
begin
  if (CurStep = ssPostInstall) and WebView2Missing() then
    MsgBox('Mailsense needs the Microsoft Edge WebView2 runtime. Windows 11 and current Windows 10 include it; if the app does not start, install the Evergreen runtime from Microsoft and run Mailsense again.', mbInformation, MB_OK);
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  DataDir: String;
begin
  if (CurUninstallStep = usPostUninstall) and not UninstallSilent then
  begin
    DataDir := ExpandConstant('{localappdata}\Mailsense');
    MsgBox('Mailsense has been uninstalled.' + #13#10 + #13#10 +
      'Your mail, settings and models remain in ' + DataDir + '.' + #13#10 +
      'Delete that folder yourself only when you no longer need that data.',
      mbInformation, MB_OK);
  end;
end;
