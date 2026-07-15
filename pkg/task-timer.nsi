; Task Timer Windows installer.
;
; Every path is injected by pkg/package-exe.sh via -D defines:
;   OUTFILE   absolute path of the installer to write (under build/dist)
;   STAGING   absolute path of build/staging/nsis/<arch>, holding the payload
;   VERSION   version string
;   ARCH      CPU the bundled binaries are built for: amd64 or arm64
;   ICONFILE  absolute path of TaskTimer.ico (optional)
;
; The .nsi never hardcodes a relative path such as ..\dist\... .
;
; ARCH is the architecture of the PAYLOAD, not of this installer: NSIS always
; emits a 32-bit x86 stub, which both x64 and ARM64 Windows run (emulated on the
; latter). So the stub happily starts anywhere and would cheerfully unpack arm64
; binaries onto an x64 box, where they cannot run -- .onInit compares ARCH
; against the real CPU and refuses instead.

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

!define APP_NAME    "Task Timer"
!define APP_EXE     "task-timer.exe"
!define SYNC_EXE    "task-timer-sync.exe"
!define PUBLISHER   "Task Timer"
!define REG_UNINST  "Software\Microsoft\Windows\CurrentVersion\Uninstall\TaskTimer"
!define REG_APP     "Software\TaskTimer"

Name "${APP_NAME} (${ARCH})"
OutFile "${OUTFILE}"
; $PROGRAMFILES64 is the *native* Program Files on both x64 and ARM64 Windows
; (C:\Program Files), not the (x86) dir this 32-bit stub would otherwise get.
; The install dir is read back from the registry in .onInit, once the 64-bit
; registry view is selected -- InstallDirRegKey would run before that and read
; the wrong (WOW6432Node) view.
InstallDir "$PROGRAMFILES64\TaskTimer"
RequestExecutionLevel admin
SetCompressor /SOLID lzma

VIProductVersion "${VERSION}.0"
VIAddVersionKey "ProductName"     "${APP_NAME}"
VIAddVersionKey "FileDescription" "${APP_NAME} installer"
VIAddVersionKey "FileVersion"     "${VERSION}"
VIAddVersionKey "ProductVersion"  "${VERSION}"
VIAddVersionKey "CompanyName"     "${PUBLISHER}"
VIAddVersionKey "LegalCopyright"  "${PUBLISHER}"

; Installer / uninstaller icon (only if icongen produced one).
!ifdef ICONFILE
  !define MUI_ICON   "${ICONFILE}"
  !define MUI_UNICON "${ICONFILE}"
!endif

; The components page is what makes the optional sync-at-login section
; selectable; without it, an /o section can never be turned on.
!insertmacro MUI_PAGE_COMPONENTS
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"

; The binaries are 64-bit and live in the 64-bit Program Files, so their
; registry footprint belongs in the 64-bit view too. Without SetRegView 64 this
; 32-bit stub would silently write every key under WOW6432Node, and the 64-bit
; uninstaller entry Add/Remove Programs looks for would simply not be there.
Function .onInit
  SetRegView 64

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

  ; Reuse the previous install dir, now that the 64-bit view is selected.
  ReadRegStr $R1 HKLM "${REG_APP}" "InstallDir"
  ${If} $R1 != ""
    StrCpy $INSTDIR $R1
  ${EndIf}
FunctionEnd

Function un.onInit
  SetRegView 64
FunctionEnd

Section "Install" SecInstall
  SetOutPath "$INSTDIR"

  ; Both binaries ship together.
  File "${STAGING}\${APP_EXE}"
  File "${STAGING}\${SYNC_EXE}"

!ifdef ICONFILE
  File "${STAGING}\TaskTimer.ico"
  CreateDirectory "$SMPROGRAMS\${APP_NAME}"
  CreateShortCut "$SMPROGRAMS\${APP_NAME}\${APP_NAME}.lnk" \
    "$INSTDIR\${APP_EXE}" "" "$INSTDIR\TaskTimer.ico" 0
  CreateShortCut "$SMPROGRAMS\${APP_NAME}\Uninstall ${APP_NAME}.lnk" \
    "$INSTDIR\uninstall.exe"
  CreateShortCut "$DESKTOP\${APP_NAME}.lnk" \
    "$INSTDIR\${APP_EXE}" "" "$INSTDIR\TaskTimer.ico" 0
!else
  CreateDirectory "$SMPROGRAMS\${APP_NAME}"
  CreateShortCut "$SMPROGRAMS\${APP_NAME}\${APP_NAME}.lnk" "$INSTDIR\${APP_EXE}"
  CreateShortCut "$SMPROGRAMS\${APP_NAME}\Uninstall ${APP_NAME}.lnk" \
    "$INSTDIR\uninstall.exe"
  CreateShortCut "$DESKTOP\${APP_NAME}.lnk" "$INSTDIR\${APP_EXE}"
!endif

  WriteUninstaller "$INSTDIR\uninstall.exe"

  WriteRegStr HKLM "${REG_APP}" "InstallDir" "$INSTDIR"
  WriteRegStr HKLM "${REG_APP}" "Version" "${VERSION}"
  WriteRegStr HKLM "${REG_APP}" "Arch" "${ARCH}"

  ; Add/Remove Programs entry.
  WriteRegStr   HKLM "${REG_UNINST}" "DisplayName"     "${APP_NAME}"
  WriteRegStr   HKLM "${REG_UNINST}" "DisplayVersion"  "${VERSION}"
  WriteRegStr   HKLM "${REG_UNINST}" "Publisher"       "${PUBLISHER}"
  WriteRegStr   HKLM "${REG_UNINST}" "UninstallString" "$\"$INSTDIR\uninstall.exe$\""
  WriteRegStr   HKLM "${REG_UNINST}" "QuietUninstallString" "$\"$INSTDIR\uninstall.exe$\" /S"
  WriteRegStr   HKLM "${REG_UNINST}" "InstallLocation" "$INSTDIR"
!ifdef ICONFILE
  WriteRegStr   HKLM "${REG_UNINST}" "DisplayIcon"     "$INSTDIR\TaskTimer.ico"
!else
  WriteRegStr   HKLM "${REG_UNINST}" "DisplayIcon"     "$INSTDIR\${APP_EXE}"
!endif
  WriteRegDWORD HKLM "${REG_UNINST}" "NoModify" 1
  WriteRegDWORD HKLM "${REG_UNINST}" "NoRepair" 1
SectionEnd

; The sync daemon runs at login, or it never runs at all. Shipping the binary
; with nothing to start it is how a backend ends up installed but dead.
;
; It is a separate, *unselected* component rather than part of the main install:
; it polls the user's issue tracker over the network, and that is an opt-in, not
; something an installer decides on their behalf. Unchecked, the daemon is still
; installed and can be started by hand at any time.
Section /o "Run the sync daemon at login" SecSyncAtLogin
  ; A Startup shortcut, deliberately *not* an Exec here. The installer runs
  ; elevated (RequestExecutionLevel admin), so launching the daemon from it would
  ; run it as Administrator — and the daemon keeps its database and its token in
  ; the *user's* profile. It would quietly sync into the wrong account's data
  ; directory. The shortcut runs it as the real user at their next sign-in.
  CreateShortCut "$SMSTARTUP\${APP_NAME} Sync.lnk" "$INSTDIR\${SYNC_EXE}"
SectionEnd

LangString DESC_SecInstall     ${LANG_ENGLISH} \
  "The Task Timer desktop app and the sync daemon."
LangString DESC_SecSyncAtLogin ${LANG_ENGLISH} \
  "Start the sync daemon automatically at login. It does nothing until you enable a provider in Settings. Your API token goes in sync.env in the data directory."

!insertmacro MUI_FUNCTION_DESCRIPTION_BEGIN
  !insertmacro MUI_DESCRIPTION_TEXT ${SecInstall}     $(DESC_SecInstall)
  !insertmacro MUI_DESCRIPTION_TEXT ${SecSyncAtLogin} $(DESC_SecSyncAtLogin)
!insertmacro MUI_FUNCTION_DESCRIPTION_END

Section "Uninstall"
  ; Stop the daemon before deleting it out from under itself.
  ExecWait 'taskkill /F /IM "${SYNC_EXE}"'

  Delete "$INSTDIR\${APP_EXE}"
  Delete "$INSTDIR\${SYNC_EXE}"
  Delete "$INSTDIR\TaskTimer.ico"
  Delete "$INSTDIR\uninstall.exe"
  RMDir "$INSTDIR"

  Delete "$DESKTOP\${APP_NAME}.lnk"
  Delete "$SMSTARTUP\${APP_NAME} Sync.lnk"
  Delete "$SMPROGRAMS\${APP_NAME}\${APP_NAME}.lnk"
  Delete "$SMPROGRAMS\${APP_NAME}\Uninstall ${APP_NAME}.lnk"
  RMDir "$SMPROGRAMS\${APP_NAME}"

  DeleteRegKey HKLM "${REG_UNINST}"
  DeleteRegKey HKLM "${REG_APP}"
SectionEnd
