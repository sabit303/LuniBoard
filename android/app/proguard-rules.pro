# kotlinx.serialization: keep generated serializers of protocol and storage models.
-keepattributes *Annotation*, InnerClasses
-keep,includedescriptorclasses class com.luniboard.**$$serializer { *; }
-keepclassmembers class com.luniboard.** {
    *** Companion;
}
-keepclasseswithmembers class com.luniboard.** {
    kotlinx.serialization.KSerializer serializer(...);
}
# OkHttp optional platform integrations
-dontwarn org.conscrypt.**
-dontwarn org.bouncycastle.**
-dontwarn org.openjsse.**
