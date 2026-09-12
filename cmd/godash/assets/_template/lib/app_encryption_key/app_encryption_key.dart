// Code as template. DO NOT EDIT.

import 'dart:async';
import 'dart:math';
import 'dart:convert';
import 'dart:typed_data';

import 'package:flutter_secure_storage/flutter_secure_storage.dart';

class AppEncryptionKey {
  static Future<String> key() async {
    const appEncryptionKey = 'aek';
    const storage = FlutterSecureStorage();
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
