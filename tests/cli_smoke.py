"""在原生宿主验证已构建程序；不连接机器人，不调用模型。"""
import hashlib
import json
import os
import platform
from pathlib import Path
import socket
import subprocess
import tempfile
import time
import tomllib
import urllib.request


def run(*args, expected=0, env=None):
    result = subprocess.run(args, capture_output=True, text=True, env=env, timeout=30)
    if result.returncode != expected:
        raise RuntimeError(f'{Path(args[0]).name}: unexpected exit {result.returncode}: {result.stderr}')
    return result.stdout


def main():
    binary = Path('wecom-inbox').resolve()
    root = Path(tempfile.mkdtemp(prefix='wecom-inbox-native-'))
    config_path = root / 'config.toml'
    run(str(binary), 'init', '-c', str(config_path))
    run(str(binary), 'check', '-c', str(config_path))
    report = json.loads(run(str(binary), 'import', '-c', str(config_path),
                            '--file', 'tests/fixtures/legacy/messages.jsonl',
                            '--media-root', 'tests/fixtures/legacy', expected=1))
    assert report['imported'] == 4 and report['failed_attachments'] == 1, report
    run(str(binary), 'backup', '-c', str(config_path), '--out', str(root / 'backup'))
    run(str(binary), 'restore', '--from', str(root / 'backup'), '--out', str(root / 'restored'))

    checksums = root / 'SHA256SUMS'
    checksums.write_text(hashlib.sha256(binary.read_bytes()).hexdigest() + '  wecom-inbox\n')
    install_dir = root / 'bin'
    run('./install.sh', str(binary), str(checksums), env={**os.environ, 'INBOX_INSTALL_DIR': str(install_dir)})
    run(str(install_dir / 'wecom-inbox'), 'version')

    # 独立端口和配置，避免借用宿主上已有服务的结果。
    with socket.socket() as reservation:
        reservation.bind(('127.0.0.1', 0))
        port = reservation.getsockname()[1]
    config_path.write_text(config_path.read_text().replace('127.0.0.1:8787', f'127.0.0.1:{port}'))
    config = tomllib.loads(config_path.read_text())
    process = subprocess.Popen([str(binary), 'serve', '-c', str(config_path)],
                               stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    try:
        endpoint = f'http://127.0.0.1:{port}'
        for attempt in range(100):
            if process.poll() is not None:
                raise RuntimeError('no-Bot service exited before readiness')
            try:
                with urllib.request.urlopen(endpoint + '/healthz', timeout=1) as response:
                    if json.load(response)['ok']:
                        break
            except OSError:
                time.sleep(0.1)
        else:
            raise RuntimeError('no-Bot service did not become ready')
        with urllib.request.urlopen(endpoint, timeout=3) as response:
            assert b'WeCom Inbox' in response.read()
        request = urllib.request.Request(endpoint + '/v1/messages',
                                         headers={'Authorization': 'Bearer ' + config['admin_token']})
        with urllib.request.urlopen(request, timeout=3) as response:
            records = json.load(response)['messages']
        assert len(records) == 4
        assert sum(record['status'] == 'partial' for record in records) == 1
    finally:
        process.terminate()
        try:
            process.communicate(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.communicate(timeout=5)
    print(json.dumps({'os': platform.system(), 'arch': platform.machine(),
                      'native_install': 'pass', 'no_bot_http': 'pass',
                      'import': report, 'backup_restore': 'pass'}))


if __name__ == '__main__':
    main()
