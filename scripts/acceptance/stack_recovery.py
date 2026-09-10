#!/usr/bin/env python3
"""Isolated single-host acceptance; never reads .env or venue credentials.

Requires built BOLTER_IMAGE/BOLTER_WEB_IMAGE and Docker Compose. Optionally
exercise real public BBO streams with --public-seconds 180. Only an explicitly
supplied GATEWAY_HTTP_PROXY is forwarded. All containers, volumes and generated
service credentials are removed in finally; the report contains no secrets.
"""
import argparse
from contextlib import ExitStack
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parents[2]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--public-seconds', type=int, default=0)
    parser.add_argument('--output', type=Path, default=ROOT / 'output/u6-stack')
    args = parser.parse_args()
    if args.public_seconds and args.public_seconds < 120:
        parser.error('public run must allow at least 120 seconds for recovery')
    args.output.mkdir(parents=True, exist_ok=True)
    report = {'checks': [], 'resources': []}
    # Allowlist excludes ambient application tokens and venue keys.
    env = {k: os.environ[k] for k in ('PATH', 'HOME', 'DOCKER_HOST', 'DOCKER_CONTEXT', 'GOCACHE',
           'BOLTER_IMAGE', 'BOLTER_WEB_IMAGE', 'GATEWAY_HTTP_PROXY') if k in os.environ}
    env['BT_LIVE_PRIVATE_WRITES_ENABLED'] = 'false'
    # Docker assigns a new published port on each start when host port is 0.
    # Pick unused loopback ports once so clients keep the same URL on restart.
    with ExitStack() as ports:
        for name in ('POSTGRES_PORT', 'GATEWAY_PORT', 'TRADING_PORT', 'AUTHEDGE_PORT', 'WEB_PORT'):
            listener = ports.enter_context(socket.socket())
            listener.bind(('127.0.0.1', 0))
            env[name] = str(listener.getsockname()[1])
    monitor = None
    with tempfile.TemporaryDirectory(prefix='bolter-u6-') as temp, (args.output / 'operations.log').open('w') as log:
        temp = Path(temp)
        env['BT_SECRET_DIR'] = str(temp / '.secrets')
        project = 'bolter-u6-' + str(os.getpid())
        compose = ['docker', 'compose', '--env-file', str(temp / '.env.stack'), '-f', str(ROOT / 'compose.yaml'), '-p', project]

        def run(command, *, capture=False, input=None, stdout=None, timeout=240):
            result = subprocess.run(command, cwd=ROOT, env=env, input=input, stdout=subprocess.PIPE if capture else stdout or log,
                                    stderr=log, timeout=timeout, check=True)
            return result.stdout.decode().strip() if capture else None

        def dc(*command, **kwargs):
            return run(compose + list(command), **kwargs)

        def passed(check):
            report['checks'].append(check)
            print(check, flush=True)

        def endpoint(service, port):
            address = dc('port', service, str(port), capture=True)
            if not address.startswith('127.0.0.1:'):
                raise RuntimeError('acceptance port is not loopback')
            return 'http://' + address

        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

        def status(url, token=''):
            request = urllib.request.Request(url, headers={'Authorization': 'Bearer ' + token} if token else {})
            try:
                with opener.open(request, timeout=4) as response:
                    return response.status, response.read()
            except urllib.error.HTTPError as error:
                return error.code, error.read()
            except (OSError, urllib.error.URLError):
                return 0, b''

        def wait_ready(url, token='', seconds=150):
            end = time.monotonic() + seconds
            while time.monotonic() < end:
                code, body = status(url, token)
                if code == 200:
                    return body
                time.sleep(1)
            raise RuntimeError('readiness did not recover: ' + url)

        def sql(database, statement):
            return dc('exec', '-T', 'postgres', 'psql', '-U', 'bolter_bootstrap', '-d', database,
                      '-v', 'ON_ERROR_STOP=1', '-tAc', statement, capture=True)

        def resource_sample(label):
            ids = dc('ps', '-q', capture=True).splitlines()
            raw = run(['docker', 'stats', '--no-stream', '--format', '{{json .}}'] + ids, capture=True)
            code, metrics = status(gateway + '/metrics', token)
            selected = [line for line in metrics.decode().splitlines() if line.startswith(('gateway_market_hot_instruments', 'go_goroutines ', 'process_resident_memory_bytes '))]
            report['resources'].append({'label': label, 'containers': [json.loads(row) for row in raw.splitlines()], 'metrics_status': code, 'metrics': selected})
            if label in ('subscribed', 'unsubscribed'):
                assert code == 200
                hot = [float(line.rsplit(' ', 1)[1]) for line in selected if line.startswith('gateway_market_hot_instruments{')]
                assert len(hot) == 2, 'missing or unexpected market owner metrics'
                assert sum(hot) == (2 if label == 'subscribed' else 0), 'hot instrument demand did not match subscription lifecycle'

        try:
            run(['go', 'run', './internal/devsetup/cmd/bootstrap', '-root', str(temp), '-quiet'])
            env.pop('GATEWAY_HTTP_PROXY', None)  # honor Bootstrap's Docker-address normalization
            dc('up', '-d', '--no-build', '--wait', '--wait-timeout', '240', timeout=300)
            gateway, trading, web = endpoint('gateway', 18080), endpoint('trading', 18092), endpoint('web', 8080)
            token = (temp / '.secrets/stack/gateway_auth_token').read_text().strip()
            trading_token = (temp / '.secrets/stack/trading_service_token').read_text().strip()
            gateway_ready, trading_ready = gateway + '/v1/health/ready', trading + '/api/v1/system/readiness'
            wait_ready(gateway_ready, token)
            wait_ready(trading_ready, trading_token)
            wait_ready(web)
            assert sql('bolter_gateway', 'SELECT count(*) FROM gateway_accounts') == '0'
            passed('empty-account stack: migrations and all HTTP readiness checks passed')
            resource_sample('initial')

            if args.public_seconds:
                monitor_binary = temp / 'gateway-monitor'
                run(['go', 'build', '-o', str(monitor_binary), './cmd/gateway-monitor'])
                code, body = status(gateway + '/v1/market/instruments', token)
                assert code == 200
                catalog = json.loads(body)
                report['catalog'] = {'count': len(catalog['items']), 'complete': catalog['complete'],
                                     'unavailable_profiles': catalog['unavailable_profiles']}
                instruments = []
                for venue, product in [('binance', 'spot'), ('okx', 'perp')]:
                    matches = [x['instrument_id'] for x in catalog['items'] if x['venue'] == venue and
                               x['product'] == product and x['base_asset'] == 'BTC' and x['quote_asset'] == 'USDT']
                    if len(matches) != 1:
                        raise RuntimeError('public acceptance BTC scope unavailable: ' + venue)
                    instruments.extend(matches)
                report['instruments'] = instruments
                command = [str(monitor_binary), '-gateway', gateway, '-state', str(temp / 'monitor.json'),
                           '-duration', str(args.public_seconds) + 's', '-window', '10s', '-change-percent', '0.001',
                           '-min-samples', '3', '-max-gap', '5s', '-cooldown', '10s']
                for instrument in instruments:
                    command += ['-instrument', instrument]
                monitor_env = dict(env, GATEWAY_TOKEN=token)
                monitor_output = (args.output / 'monitor.jsonl').open('w')
                monitor = subprocess.Popen(command, cwd=ROOT, env=monitor_env, stdout=monitor_output, stderr=log)
                time.sleep(20)
                resource_sample('subscribed')

            for service, url, auth in [('gateway', gateway_ready, token), ('trading', trading_ready, trading_token)]:
                dc('stop', '-t', '5', service)
                assert status(url, auth)[0] != 200
                dc('start', service)
                wait_ready(url, auth)
                passed(service + ' stop/restart recovered')

            sql('bolter_bootstrap', 'ALTER DATABASE bolter_trading ALLOW_CONNECTIONS false')
            try:
                sql('bolter_bootstrap', "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='bolter_trading'")
                code, body = status(trading_ready, trading_token)
                assert code == 503
                assert not json.loads(body)['data']['components']['database']['ready']
                assert status(gateway_ready, token)[0] == 200
            finally:
                sql('bolter_bootstrap', 'ALTER DATABASE bolter_trading ALLOW_CONNECTIONS true')
            wait_ready(trading_ready, trading_token)
            passed('Trading database outage was detected independently of a healthy Gateway, then recovered')

            dc('stop', '-t', '5', 'postgres')
            try:
                # Readiness must stop claiming the database-dependent service is usable.
                assert status(gateway_ready, token)[0] != 200
                assert status(trading_ready, trading_token)[0] != 200
            finally:
                dc('start', 'postgres')
            wait_ready(gateway_ready, token)
            wait_ready(trading_ready, trading_token)
            passed('temporary PostgreSQL outage denied readiness, then recovered without service restart')

            if monitor is not None:
                monitor.wait(timeout=args.public_seconds + 30)
                monitor_output.close()
                if monitor.returncode:
                    raise RuntimeError('public monitor exited unsuccessfully')
                events = [json.loads(x) for x in (args.output / 'monitor.jsonl').read_text().splitlines()]
                assert events and len({e['id'] for e in events}) == len(events)
                report['monitor_events'] = len(events)
                report['monitor_state'] = json.loads((temp / 'monitor.json').read_text())
                for instrument in instruments:
                    state = report['monitor_state']['instruments'][instrument]
                    assert len(state['samples']) >= 3, 'no restored public observation window: ' + instrument
                report['monitor_event_types'] = sorted({x.get('type', x.get('kind', '')) for x in events})
                passed('bounded public monitor survived service and database restarts')
                monitor = None
                time.sleep(35)  # production Hub idle grace is 30 seconds
                resource_sample('unsubscribed')

            # Stop writers to make the three dumps a consistent maintenance snapshot.
            dc('stop', '-t', '10', 'web', 'authedge', 'trading', 'gateway')
            backup = temp / 'backup'
            backup.mkdir(mode=0o700)
            shutil.copytree(temp / '.secrets', backup / '.secrets')
            shutil.copy2(temp / '.env.stack', backup / '.env.stack')
            for service in ('gateway', 'trading', 'authedge'):
                database = 'bolter_' + service
                sql(database, "CREATE TABLE u6_backup_probe(value text PRIMARY KEY); INSERT INTO u6_backup_probe VALUES ('verified')")
                dump = backup / (database + '.dump')
                with dump.open('wb') as output:
                    dc('exec', '-T', 'postgres', 'pg_dump', '-U', 'bolter_bootstrap', '-Fc', database, stdout=output)
                restored = 'u6_restore_' + service
                owner = 'bolter_gateway_migrator' if service == 'gateway' else 'bolter_' + service
                sql('bolter_bootstrap', 'CREATE DATABASE ' + restored + ' OWNER ' + owner)
                dc('exec', '-T', 'postgres', 'pg_restore', '-U', 'bolter_bootstrap', '--exit-on-error', '-d', restored, input=dump.read_bytes())
                assert sql(restored, 'SELECT value FROM u6_backup_probe') == 'verified'
                assert sql(restored, "SELECT count(*) FROM information_schema.tables WHERE table_schema='public'") == sql(database, "SELECT count(*) FROM information_schema.tables WHERE table_schema='public'")
            assert (backup / '.secrets/stack/secret_master_key').read_bytes() == (temp / '.secrets/stack/secret_master_key').read_bytes()
            passed('three database dumps restored into separate databases; encryption key backup preserved')
            report['complete'] = True
        finally:
            if monitor is not None and monitor.poll() is None:
                monitor.terminate()
                try:
                    monitor.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    monitor.kill()
                    monitor.wait()
            subprocess.run(compose + ['down', '--volumes', '--remove-orphans'], cwd=ROOT, env=env,
                           stdout=log, stderr=log, timeout=90, check=False)
            (args.output / 'report.json').write_text(json.dumps(report, indent=2) + '\n')


if __name__ == '__main__':
    main()
