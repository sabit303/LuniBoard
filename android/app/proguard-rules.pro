# kotlinx.serialization: keep generated serializers of protocol and storage models.
-keepattributes *Annotation*, InnerClasses
-keep,includedescriptorclasses class com.universalclipboard.**$$serializer { *; }
-keepclassmembers class com.universalclipboard.** {
    *** Companion;
}
-keepclasseswithmembers class com.universalclipboard.** {
    kotlinx.serialization.KSerializer serializer(...);
}
# OkHttp optional platform integrations
-dontwarn org.conscrypt.**
-dontwarn org.bouncycastle.**
-dontwarn org.openjsse.**
