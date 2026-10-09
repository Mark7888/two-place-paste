; NSIS installer for the TwoPlacePaste desktop service.
;
; Per-user, not per-machine: the service holds one person's clipboard, runs as
; that person, and writes its login item to HKCU (SPEC §7.2). A per-machine
; install would need administrator rights for a program that needs none.

!include "MUI2.nsh"

!ifndef VERSION
  !define VERSION "0.1.0"
!endif
!ifndef SOURCE_EXE
  !define SOURCE_EXE "tppdesktop.exe"
!endif
; CI passes an absolute path; makensis otherwise writes next to this script.
!ifndef OUTFILE
  !define OUTFILE "TwoPlacePaste-Setup.exe"
!endif

Name "TwoPlacePaste"
OutFile "${OUTFILE}"
Unicode true
RequestExecutionLevel user
InstallDir "$LOCALAPPDATA\Programs\TwoPlacePaste"
InstallDirRegKey HKCU "Software\TwoPlacePaste" "InstallDir"
ShowInstDetails show
ShowUnInstDetails show

VIProductVersion "${VERSION}.0"
VIAddVersionKey "ProductName" "TwoPlacePaste"
VIAddVersionKey "FileDescription" "TwoPlacePaste desktop service"
VIAddVersionKey "FileVersion" "${VERSION}"
VIAddVersionKey "ProductVersion" "${VERSION}"
VIAddVersionKey "LegalCopyright" ""

; The installer, uninstaller and shortcut all wear the app's own icon; the
; one inside tppdesktop.exe comes from cmd/tppdesktop/winres.
!define MUI_ICON "app.ico"
!define MUI_UNICON "app.ico"

!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"

Section "TwoPlacePaste" SecMain
  SetOutPath "$INSTDIR"
  File "/oname=tppdesktop.exe" "${SOURCE_EXE}"

  WriteRegStr HKCU "Software\TwoPlacePaste" "InstallDir" "$INSTDIR"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\TwoPlacePaste" \
    "DisplayName" "TwoPlacePaste"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\TwoPlacePaste" \
    "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\TwoPlacePaste" \
    "DisplayIcon" "$INSTDIR\tppdesktop.exe,0"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\TwoPlacePaste" \
    "UninstallString" "$\"$INSTDIR\Uninstall.exe$\""
  WriteRegDWORD HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\TwoPlacePaste" \
    "NoModify" 1

  CreateShortCut "$SMPROGRAMS\TwoPlacePaste.lnk" "$INSTDIR\tppdesktop.exe" "" "$INSTDIR\tppdesktop.exe" 0
  WriteUninstaller "$INSTDIR\Uninstall.exe"
SectionEnd

Section "Uninstall"
  Delete "$SMPROGRAMS\TwoPlacePaste.lnk"
  Delete "$INSTDIR\tppdesktop.exe"
  Delete "$INSTDIR\Uninstall.exe"
  RMDir "$INSTDIR"

  ; The login item is this installer's to remove; the user's settings and keys
  ; are not, and are left where they are.
  DeleteRegValue HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "com.twoplacepaste.desktop"
  DeleteRegKey HKCU "Software\TwoPlacePaste"
  DeleteRegKey HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\TwoPlacePaste"
SectionEnd
