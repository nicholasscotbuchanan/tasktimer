; Task Timer gateway (server) Windows installer.
;
; Every path is injected by pkg/package-server-exe.sh via -D defines:
;   OUTFILE   absolute path of the installer to write (under build/dist)
;   STAGING   absolute path of build/staging/nsis-server/<arch>, holding payload
;   VERSION   version string
;   ARCH      CPU the bundled .exe is built for: amd64 or arm64
;   ICONFILE  absolute path of TaskTimer.ico (optional)
;
; This is the SERVER, not the desktop client. It installs one .exe and registers
; it as a Windows service - the analogue of the systemd unit the .deb ships. The
; service reads its config and secrets from %ProgramData%\TaskTimerServer (see
; paths_windows.go), which is where this installer writes the starter config.
;
; ARCH is the architecture of the PAYLOAD, not of this installer: NSIS always
; emits a 32-bit x86 stub, which both x64 and ARM64 Windows run (emulated on the
; latter). .onInit compares ARCH against the real CPU and refuses to unpack an
; arm64 service onto an x64 box, where it could not run.

!include "MUI2.nsh"
!include "LogicLib.nsh"
!include "x64.nsh"

!ifndef OUTFILE
  !error "OUTFILE must be passed with -DOUTFILE=<abs path>"
!endif
!ifndef STAGING
  !error "STAGING must be passed with -DSTAGING=<abs path>"
!endif
!ifndef VERSION
  !define VERSION "1.0.0"
!endif
!ifndef ARCH
  !error "ARCH must be passed with -DARCH=<amd64|arm64>"
!endif

!define APP_NAME    "Task Timer Server"
!define SERVER_EXE  "task-timer-server.exe"
!define SVC_NAME    "TaskTimerServer"
!define PUBLISHER   "Task Timer"
!define REG_UNINST  "Software\Microsoft\Windows\CurrentVersion\Uninstall\TaskTimerServer"
!define REG_APP     "Software\TaskTimerServer"
!define DATA_SUBDIR "TaskTimerServer"

Name "${APP_NAME} (${ARCH})"
OutFile "${OUTFILE}"
; $PROGRAMFILES64 is the native Program Files on both x64 and ARM64 Windows.
InstallDir "$PROGRAMFILES64\TaskTimerServer"
; Registering a service and writing under %ProgramData% both need elevation.
RequestExecutionLevel admin
SetCompressor /SOLID lzma

VIProductVersion "${VERSION}.0"
VIAddVersionKey "ProductName"     "${APP_NAME}"
VIAddVersionKey "FileDescription" "${APP_NAME} installer"
VIAddVersionKey "FileVersion"     "${VERSION}"
VIAddVersionKey "ProductVersion"  "${VERSION}"
VIAddVersionKey "CompanyName"     "${PUBLISHER}"
VIAddVersionKey "LegalCopyright"  "${PUBLISHER}"

!ifdef ICONFILE
  !define MUI_ICON   "${ICONFILE}"
  !define MUI_UNICON "${ICONFILE}"
!endif

!insertmacro MUI_PAGE_COMPONENTS
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"

; The .exe is 64-bit and lives in the 64-bit Program Files, so its registry
; footprint belongs in the 64-bit view. SetShellVarContext all makes $APPDATA
; resolve to C:\ProgramData (machine-wide) rather than the installing admin's
; roaming profile - the service runs as LocalSystem and reads the machine copy.
Function .onInit
  SetRegView 64
  SetShellVarContext all

  ${If} ${IsNativeARM64}
    StrCpy $R0 "arm64"
  ${ElseIf} ${IsNativeAMD64}
    StrCpy $R0 "amd64"
  ${Else}
    StrCpy $R0 "unsupported"
  ${EndIf}

  ${If} $R0 != "${ARCH}"
    MessageBox MB_OK|MB_ICONSTOP \
      "This is the ${ARCH} build of ${APP_NAME}, which cannot run on this PC ($R0).$\r$\n$\r$\nDownload the $R0 installer instead."
    Abort
  ${EndIf}

  ReadRegStr $R1 HKLM "${REG_APP}" "InstallDir"
  ${If} $R1 != ""
    StrCpy $INSTDIR $R1
  ${EndIf}
FunctionEnd

Function un.onInit
  SetRegView 64
  SetShellVarContext all
FunctionEnd

Section "Task Timer Server (service)" SecInstall
  SectionIn RO
  SetOutPath "$INSTDIR"

  File "${STAGING}\${SERVER_EXE}"
!ifdef ICONFILE
  File "${STAGING}\TaskTimer.ico"
!endif

  ; Starter config into %ProgramData%\TaskTimerServer, but never clobber an
  ; edited one on upgrade - it holds the operator's client_id and public_url.
  CreateDirectory "$APPDATA\${DATA_SUBDIR}"
  ${IfNot} ${FileExists} "$APPDATA\${DATA_SUBDIR}\config.toml"
    SetOutPath "$APPDATA\${DATA_SUBDIR}"
    File "/oname=config.toml" "${STAGING}\config.example.toml"
    SetOutPath "$INSTDIR"
  ${EndIf}

  ; A README with the two-secrets setup, alongside the binary.
  ClearErrors
  FileOpen $9 "$INSTDIR\README.txt" w
  ${IfNot} ${Errors}
    FileWrite $9 "Task Timer Server (${VERSION})$\r$\n"
    FileWrite $9 "==============================$\r$\n$\r$\n"
    FileWrite $9 "The service '${SVC_NAME}' is installed but will NOT start until it is$\r$\n"
    FileWrite $9 "configured. It reads everything from:$\r$\n$\r$\n"
    FileWrite $9 "    %ProgramData%\${DATA_SUBDIR}\$\r$\n$\r$\n"
    FileWrite $9 "1. Edit config.toml there (set client_id and public_url).$\r$\n$\r$\n"
    FileWrite $9 "2. Drop in the two secrets as files in that folder:$\r$\n$\r$\n"
    FileWrite $9 "     atlassian_client_secret   (from the Atlassian dev console)$\r$\n"
    FileWrite $9 "     token_encryption_key      (generate it once, see below)$\r$\n$\r$\n"
    FileWrite $9 "   Generate the key from an elevated prompt:$\r$\n$\r$\n"
    FileWrite $9 '     "$INSTDIR\${SERVER_EXE}" gen-key > "%ProgramData%\${DATA_SUBDIR}\token_encryption_key"$\r$\n$\r$\n'
    FileWrite $9 "   Back it up and NEVER regenerate it: it decrypts the stored Jira$\r$\n"
    FileWrite $9 "   refresh tokens, and a new key invalidates every one of them.$\r$\n$\r$\n"
    FileWrite $9 "3. Start it:   sc start ${SVC_NAME}   (or reboot; it is set to auto-start)$\r$\n"
    FileWrite $9 "   Manage it:  $\"$INSTDIR\${SERVER_EXE}$\" service <start|stop|uninstall>$\r$\n$\r$\n"
    FileWrite $9 "Logs go to %ProgramData%\${DATA_SUBDIR}\server.log when run as a service.$\r$\n"
    FileClose $9
  ${EndIf}

  ; Register the Windows service. The binary registers itself: its own path
  ; becomes the service ImagePath, and svc.IsWindowsService routes it into the
  ; service runner on start. Not started here - it has no secrets yet.
  DetailPrint "Registering the ${SVC_NAME} service..."
  nsExec::ExecToLog '"$INSTDIR\${SERVER_EXE}" service install'
  Pop $0
  ${If} $0 != 0
    MessageBox MB_OK|MB_ICONEXCLAMATION \
      "The ${SVC_NAME} service could not be registered (exit $0).$\r$\n$\r$\nYou can register it later from an elevated prompt:$\r$\n    $\"$INSTDIR\${SERVER_EXE}$\" service install"
  ${EndIf}

  WriteUninstaller "$INSTDIR\uninstall.exe"

  WriteRegStr HKLM "${REG_APP}" "InstallDir" "$INSTDIR"
  WriteRegStr HKLM "${REG_APP}" "Version"    "${VERSION}"
  WriteRegStr HKLM "${REG_APP}" "Arch"       "${ARCH}"

  WriteRegStr   HKLM "${REG_UNINST}" "DisplayName"     "${APP_NAME}"
  WriteRegStr   HKLM "${REG_UNINST}" "DisplayVersion"  "${VERSION}"
  WriteRegStr   HKLM "${REG_UNINST}" "Publisher"       "${PUBLISHER}"
  WriteRegStr   HKLM "${REG_UNINST}" "UninstallString" "$\"$INSTDIR\uninstall.exe$\""
  WriteRegStr   HKLM "${REG_UNINST}" "QuietUninstallString" "$\"$INSTDIR\uninstall.exe$\" /S"
  WriteRegStr   HKLM "${REG_UNINST}" "InstallLocation" "$INSTDIR"
!ifdef ICONFILE
  WriteRegStr   HKLM "${REG_UNINST}" "DisplayIcon"     "$INSTDIR\TaskTimer.ico"
!else
  WriteRegStr   HKLM "${REG_UNINST}" "DisplayIcon"     "$INSTDIR\${SERVER_EXE}"
!endif
  WriteRegDWORD HKLM "${REG_UNINST}" "NoModify" 1
  WriteRegDWORD HKLM "${REG_UNINST}" "NoRepair" 1
SectionEnd

; Starting the service is opt-in and OFF by default: it cannot start until the
; two secrets are in place, so starting it now would just fail. Once configured,
; tick this on a re-run, or `sc start TaskTimerServer`.
Section /o "Start the service now (needs config + secrets first)" SecStart
  DetailPrint "Starting the ${SVC_NAME} service..."
  nsExec::ExecToLog '"$INSTDIR\${SERVER_EXE}" service start'
  Pop $0
  ${If} $0 != 0
    MessageBox MB_OK|MB_ICONINFORMATION \
      "The service did not start (exit $0). This is expected until you have set$\r$\n%ProgramData%\${DATA_SUBDIR}\config.toml and the two secret files.$\r$\n$\r$\nSee README.txt in the install folder."
  ${EndIf}
SectionEnd

LangString DESC_SecInstall ${LANG_ENGLISH} \
  "The Task Timer gateway and its Windows service. Reads config and secrets from %ProgramData%\TaskTimerServer."
LangString DESC_SecStart   ${LANG_ENGLISH} \
  "Start the service immediately. Leave unticked until config.toml and the two secret files are in place."

!insertmacro MUI_FUNCTION_DESCRIPTION_BEGIN
  !insertmacro MUI_DESCRIPTION_TEXT ${SecInstall} $(DESC_SecInstall)
  !insertmacro MUI_DESCRIPTION_TEXT ${SecStart}   $(DESC_SecStart)
!insertmacro MUI_FUNCTION_DESCRIPTION_END

Section "Uninstall"
  ; Stop and deregister the service before deleting the binary out from under it.
  nsExec::ExecToLog '"$INSTDIR\${SERVER_EXE}" service stop'
  nsExec::ExecToLog '"$INSTDIR\${SERVER_EXE}" service uninstall'
  ; Belt and braces in case it was still running detached.
  nsExec::ExecToLog 'taskkill /F /IM "${SERVER_EXE}"'

  Delete "$INSTDIR\${SERVER_EXE}"
  Delete "$INSTDIR\TaskTimer.ico"
  Delete "$INSTDIR\README.txt"
  Delete "$INSTDIR\uninstall.exe"
  RMDir "$INSTDIR"

  ; %ProgramData%\TaskTimerServer is deliberately LEFT: it holds the encryption
  ; key and every user's Jira grant. Deleting it silently signs everyone out.
  DetailPrint "Left %ProgramData%\${DATA_SUBDIR} in place (config, key, and Jira grants)."

  DeleteRegKey HKLM "${REG_UNINST}"
  DeleteRegKey HKLM "${REG_APP}"
SectionEnd
