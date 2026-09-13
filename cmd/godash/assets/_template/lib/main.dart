import 'dart:async';

import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';
import 'package:flutter_web_plugins/url_strategy.dart';
import 'package:logging/logging.dart';

import 'package:godash/bridge/bridge.dart';
import 'package:godashapp/app_encryption_key/app_encryption_key.dart';
import 'package:godashapp/pb/counter.godash.dart';
import 'package:godashapp/pb/counter.pb.dart';
import 'package:godashapp/pb/echo.godash.dart';
import 'package:godashapp/pb/echo.pb.dart';
import 'package:godashapp/version/version.dart';

Future<void> main() async {
  Logger.root.level = kDebugMode ? Level.CONFIG : Level.INFO;
  Logger.root.onRecord.listen((record) {
    // ignore: avoid_print
    print('Dart ${record.level.name}: ${record.time}: ${record.message}');
  });

  usePathUrlStrategy();
  WidgetsFlutterBinding.ensureInitialized();

  // `Uri.base.origin` is only valid for http/https (web); on native the base
  // URI is a file: URI and workerUrl is ignored by the native bridge.
  final workerUrl = kIsWeb
      ? '${Uri.base.origin}/worker.js?v=${GoBuildVersion.version}'
      : '';

  Bridge.configure(
    appEncryptionKey: AppEncryptionKey.key,
    workerUrl: workerUrl,
  );

  runApp(const StarterApp());
}

class StarterApp extends StatelessWidget {
  const StarterApp({super.key});

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'godash starter',
      theme: ThemeData(
        colorScheme: ColorScheme.fromSeed(seedColor: Colors.deepPurple),
        useMaterial3: true,
      ),
      home: const EchoScreen(),
    );
  }
}

class EchoScreen extends StatefulWidget {
  const EchoScreen({super.key});

  @override
  State<EchoScreen> createState() => _EchoScreenState();
}

class _EchoScreenState extends State<EchoScreen> {
  String _response = '';
  int? _counter;
  bool _busy = false;
  final _controller = TextEditingController(text: 'Hello godash!');

  @override
  void initState() {
    super.initState();
    _loadCounter();
  }

  Future<void> _loadCounter() async {
    try {
      final resp = await CounterRpcClient().get(GetCounterRequest());
      if (!mounted) return;
      setState(() => _counter = resp.value.toInt());
    } catch (e, st) {
      Logger.root.warning('counter get failed', e, st);
    }
  }

  Future<void> _incrementCounter() async {
    setState(() => _busy = true);
    try {
      final resp =
          await CounterRpcClient().increment(IncrementRequest(delta: 1));
      if (!mounted) return;
      setState(() => _counter = resp.value.toInt());
    } catch (e, st) {
      Logger.root.warning('counter increment failed', e, st);
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _sendEcho() async {
    setState(() => _busy = true);
    try {
      final client = EchoRpcClient();
      final resp = await client.echo(EchoRequest(message: _controller.text));
      setState(() => _response = resp.message);
    } catch (e, st) {
      setState(() => _response = 'Error: $e');
      Logger.root.warning('echo failed', e, st);
    } finally {
      setState(() => _busy = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(title: const Text('godash starter')),
      body: Padding(
        padding: const EdgeInsets.all(16),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            TextField(
              controller: _controller,
              decoration: const InputDecoration(labelText: 'Message'),
            ),
            const SizedBox(height: 16),
            ElevatedButton(
              onPressed: _busy ? null : _sendEcho,
              child: _busy
                  ? const SizedBox(
                      height: 16,
                      width: 16,
                      child: CircularProgressIndicator(strokeWidth: 2),
                    )
                  : const Text('Send Echo'),
            ),
            const SizedBox(height: 16),
            Text('Response:', style: Theme.of(context).textTheme.titleMedium),
            const SizedBox(height: 8),
            Text(_response),
            const Divider(height: 32),
            Text(
              'SQLite counter',
              style: Theme.of(context).textTheme.titleMedium,
            ),
            const SizedBox(height: 8),
            Text('Value: ${_counter ?? '…'}'),
            const SizedBox(height: 8),
            OutlinedButton(
              onPressed: _busy ? null : _incrementCounter,
              child: const Text('Increment'),
            ),
          ],
        ),
      ),
    );
  }

  @override
  void dispose() {
    _controller.dispose();
    super.dispose();
  }
}
