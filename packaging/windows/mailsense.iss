; Mailsense per-user installer (Inno Setup 6+).
; Build: iscc /DAppVersion=1.2.3 /DSourceExe=..\..\dist\mailsense.exe packaging\windows\mailsense.iss
; No administrator rights: the app installs under the user's LocalAppData and
; keeps its database in %LOCALAPPDATA%\Mailsense across upgrades and uninstalls.

#ifndef AppVersion
  #define AppVersion "0.0.0-dev"
#endif
#ifndef SourceExe
  #define SourceExe "..\..\dist\mailsense.exe"
#endif

; The public page list has one owner, internal/site, read by
; `go run ./cmd/gensite -pages=en|fr`. The Linux tarball consumes that output
; directly. The installer consumes the same output here: the two Exec calls
; write one generated [Files] include, so there is no second list to drift.
; The include is build output under dist/ and is never committed.
#define RepoRoot AddBackslash(SourcePath) + "..\.."
#define PagesInclude RepoRoot + "dist\docs-pages.iss"

#define GenerateEn Exec('cmd.exe', '/c del /q "' + PagesInclude + '" 2>nul & for /f "usebackq delims=" %p in (`go run ./cmd/gensite -pages=en`) do @echo Source: "..\..\%p"; DestDir: "{app}\docs"; Flags: ignoreversion >> "' + PagesInclude + '"', RepoRoot)
#if GenerateEn != 0
  #error cannot generate the public English docs list
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
AppPublisher=Mailsense
AppPublisherURL=https://github.com/LLM-4-People/mailsense
SupportURL=https://github.com/LLM-4-People/mailsense/issues
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
UninstallDisplayIcon={app}\mailsense.exe
CloseApplications=yes
RestartApplications=no

[Files]
Source: "{#SourceExe}"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\..\LICENSE"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\..\NOTICE"; DestDir: "{app}"; Flags: ignoreversion
; Generated from internal/site by the Exec calls above. Every public page in
; both languages ships, the same set the Linux tarball copies. The path is the
; one PagesInclude names, written in the expression form the ISPP manual shows
; for an include.
#include AddBackslash(SourcePath) + "..\..\dist\docs-pages.iss"

[Icons]
Name: "{group}\Mailsense"; Filename: "{app}\mailsense.exe"
Name: "{autodesktop}\Mailsense"; Filename: "{app}\mailsense.exe"; Tasks: desktopicon

[Tasks]
Name: "desktopicon"; Description: "{cm:CreateDesktopIcon}"; GroupDescription: "{cm:AdditionalIcons}"; Flags: unchecked

[Run]
Filename: "{app}\mailsense.exe"; Description: "{cm:LaunchProgram,Mailsense}"; Flags: nowait postinstall skipifsilent

[UninstallDelete]
; Only the installation directory. The database and settings live in
; %LOCALAPPDATA%\Mailsense and are intentionally kept; the uninstaller tells the
; owner how to delete them.
Type: filesandordirs; Name: "{app}"

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
