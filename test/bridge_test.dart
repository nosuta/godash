import 'package:flutter_test/flutter_test.dart';
import 'package:godash/bridge/bridge.dart';

void main() {
  test('Bridge() requires configure before first use', () {
    expect(() => Bridge(), throwsStateError);
  });
}
