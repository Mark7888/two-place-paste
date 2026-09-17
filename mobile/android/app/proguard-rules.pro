# React Native keeps its own consumer rules; this file is for what this app
# adds. The tile service is referenced only from the manifest, so R8 must not
# decide it is unused.
-keep class com.twoplacepaste.SyncTileService { *; }
