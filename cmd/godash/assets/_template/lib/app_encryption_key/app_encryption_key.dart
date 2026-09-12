// Code as template. DO NOT EDIT.

import 'dart:async';
import 'dart:math';
import 'dart:convert';
import 'dart:typed_data';

import 'package:flutter_secure_storage/flutter_secure_storage.dart';

class AppEncryptionKey {
  static Future<String> key() async {
    const appEncryptionKey = 'aek';
    // Use the file-based (legacy) keychain on macOS: the data-protection
    // keychain requires the keychain-access-groups entitlement (and thus a
    // development team), which breaks ad-hoc local debug builds. iOS/Android
    // ignore mOptions.
    const storage = FlutterSecureStorage(
      mOptions: MacOsOptions(usesDataProtectionKeychain: false),
    );
    String? aek;
    try {
      aek = await storage.read(key: appEncryptionKey);
      if (aek == null) {
        final Random secureRandom = Random.secure();
        final List<int> bytes = List<int>.generate(
          32,
          (_) => secureRandom.nextInt(256),
        );
        final b = Uint8List.fromList(bytes);
        aek = base64Encode(b);
        await storage.write(key: appEncryptionKey, value: aek);
      }
    } catch (e) {
      throw Exception('Failed to read or write app encryption key: $e');
    }
    return aek;
  }
}
