package com.zyndata.crossstream.relay.logic

import org.json.JSONObject

/**
 * What a GitHub release means to the relay app: a version to compare with the installed one
 * and one APK to download. Pure, so JVM tests cover it; the same rules as the hub's
 * internal/update package (docs/decisions.md, phase 18 "update notifications").
 */
object Updates {
    /** The public repository releases come from; the only place an APK is downloaded from. */
    const val REPOSITORY = "zyndata/cross-stream"

    /** `releases/latest` skips drafts and pre-releases. */
    const val LATEST_RELEASE_URL = "https://api.github.com/repos/$REPOSITORY/releases/latest"

    private const val ASSET_PREFIX = "https://github.com/$REPOSITORY/releases/download/"

    /** A release worth offering: its version, the release page and the APK. */
    data class Release(val version: Version, val page: String, val apkUrl: String, val apkSize: Long) {
        val tag: String get() = version.toString()
    }

    /** `X.Y.Z`, the only versions this project tags. */
    data class Version(val major: Int, val minor: Int, val patch: Int) : Comparable<Version> {
        override fun compareTo(other: Version): Int =
            compareValuesBy(this, other, { it.major }, { it.minor }, { it.patch })

        override fun toString(): String = "v$major.$minor.$patch"
    }

    private val VERSION = Regex("""^v?(\d{1,4})\.(\d{1,4})\.(\d{1,4})$""")

    /**
     * `v1.2.3` / `1.2.3` → a version; anything else (`0.0.0-dev` of a local build) → null, and
     * such a build is never offered an update: a release-signed APK could not replace it anyway.
     */
    fun version(text: String?): Version? = text?.trim()?.let { VERSION.matchEntire(it) }?.destructured
        ?.let { (a, b, c) -> Version(a.toInt(), b.toInt(), c.toInt()) }

    /** The name a downloaded APK gets (ours, never the server's). */
    fun apkName(version: Version): String = "crossstream-relay-$version.apk"

    /**
     * A file already downloaded for this release that is whole (same size as the release says):
     * installing it again needs no new download over metered data. A damaged file of the right
     * size is refused by the package installer (signature check).
     */
    fun isComplete(length: Long, apkSize: Long): Boolean = apkSize > 0 && length == apkSize

    private val APK_NAME = Regex("""^crossstream-relay-(v\d{1,4}\.\d{1,4}\.\d{1,4})\.apk$""")

    /** A downloaded APK of [installed] or older: already installed or replaced, so it can go. */
    fun isStaleApk(name: String, installed: String?): Boolean {
        val current = version(installed) ?: return false
        val apk = APK_NAME.matchEntire(name)?.groupValues?.get(1)?.let(::version) ?: return false
        return apk <= current
    }

    /** The release to offer, or null: not a release document, no APK, or not newer than [installed]. */
    fun offer(document: String, installed: String?): Release? {
        val latest = parse(document) ?: return null
        val current = version(installed) ?: return null
        return latest.takeIf { it.version > current }
    }

    /**
     * The tag of a `releases/latest` document, APK or not; null when the text is not a release
     * at all (a Wi-Fi login page answering in GitHub's place, an error page).
     */
    fun latestTag(document: String): String? {
        val json = runCatching { JSONObject(document) }.getOrNull() ?: return null
        if (json.optBoolean("draft") || json.optBoolean("prerelease")) return null
        return version(json.optString("tag_name"))?.toString()
    }

    /** The parts of a `releases/latest` document the app reads; null when it is not one. */
    fun parse(document: String): Release? {
        val json = runCatching { JSONObject(document) }.getOrNull() ?: return null
        val version = version(json.optString("tag_name")) ?: return null
        if (json.optBoolean("draft") || json.optBoolean("prerelease")) return null
        val assets = json.optJSONArray("assets") ?: return null
        for (i in 0 until assets.length()) {
            val a = assets.optJSONObject(i) ?: continue
            val name = a.optString("name")
            val url = a.optString("browser_download_url")
            if (name.startsWith("crossstream-relay-") && name.endsWith(".apk") && url.startsWith(ASSET_PREFIX)) {
                val page = json.optString("html_url").takeIf { it.startsWith("https://github.com/$REPOSITORY/") }
                    ?: "https://github.com/$REPOSITORY/releases/tag/$version"
                return Release(version, page, url, a.optLong("size"))
            }
        }
        return null
    }
}
