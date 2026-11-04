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
; Only the public pages ship: the site config excludes the engineering log,
; the release plan, and the benchmark records, and the installer must agree.
Source: "..\..\docs\index.md"; DestDir: "{app}\docs"; Flags: ignoreversion
Source: "..\..\docs\install.md"; DestDir: "{app}\docs"; Flags: ignoreversion
Source: "..\..\docs\first-run.md"; DestDir: "{app}\docs"; Flags: ignoreversion
Source: "..\..\docs\privacy.md"; DestDir: "{app}\docs"; Flags: ignoreversion
Source: "..\..\docs\troubleshooting.md"; DestDir: "{app}\docs"; Flags: ignoreversion
Source: "..\..\docs\fr\*.md"; DestDir: "{app}\docs\fr"; Flags: ignoreversion

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
