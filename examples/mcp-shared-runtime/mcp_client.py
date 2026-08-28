#!/usr/bin/env python3
"""Tiny deterministic MCP client used only to record the live Kranz demos."""

import json
import os
import subprocess
import sys


class Client:
    def __init__(self):
        binary = os.environ.get("KRANZ_BIN", "kranz")
        project_dir = os.environ.get("KRANZ_PROJECT_DIR", os.path.dirname(__file__))
        self.runtime = os.environ.get("KRANZ_RUNTIME")
        self.process = subprocess.Popen(
            [binary, "mcp"],
            cwd=project_dir,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=sys.stderr,
            text=True,
            bufsize=1,
        )
        self.next_id = 1
        self.initialize_result = self.request("initialize", {"protocolVersion": "2025-11-25", "capabilities": {}, "clientInfo": {"name": "kranz-demo", "version": "1"}})
        self.notify("notifications/initialized", {})

    def request(self, method, params):
        request_id = self.next_id
        self.next_id += 1
        self.process.stdin.write(json.dumps({"jsonrpc": "2.0", "id": request_id, "method": method, "params": params}) + "\n")
        self.process.stdin.flush()
        while True:
            response = json.loads(self.process.stdout.readline())
            if response.get("id") == request_id:
                return response["result"]

    def notify(self, method, params):
        self.process.stdin.write(json.dumps({"jsonrpc": "2.0", "method": method, "params": params}) + "\n")
        self.process.stdin.flush()

    def tool(self, name, arguments):
        arguments = dict(arguments)
        if self.runtime and name not in ("runtimes", "up", "down"):
            arguments["runtime"] = self.runtime
        result = self.request("tools/call", {"name": name, "arguments": arguments})["structuredContent"]
        error = result.get("error")
        if error and error["code"] == "confirmation_required":
            arguments = dict(arguments)
            arguments["confirmation_token"] = error["details"]["confirmation_token"]
            result = self.request("tools/call", {"name": name, "arguments": arguments})["structuredContent"]
        return result

    def resource(self, uri):
        if self.runtime and uri.startswith("kranz://") and uri not in ("kranz://runtimes", "kranz://capabilities"):
            uri = "kranz://runtimes/" + self.runtime + "/" + uri.removeprefix("kranz://")
        result = self.request("resources/read", {"uri": uri})
        return json.loads(result["contents"][0]["text"])

    def close(self):
        self.process.stdin.close()
        self.process.wait(timeout=5)


def main():
    command = sys.argv[1]
    client = Client()
    try:
        if command == "status":
            session = client.resource("kranz://session")
            status = client.tool("status", {"selectors": ["api"]})
            service = status["data"][0]
            print(f"runtime={session['session']['name']} session={session['session']['id'][:8]}")
            print(f"api {service['state']['status']} pid={service['state']['pid']}")
        elif command == "logs":
            result = client.tool("logs", {"selectors": ["api"], "tail": 4})
            for event in result["data"]["events"]:
                print(f"{event['stream']} {event['source']}: {event['text']}")
        elif command == "restart":
            result = client.tool("restart", {"selectors": ["api"]})
            print("restart targets=" + ",".join(result["data"]["plan"]["targets"]))
            waited = client.tool("wait", {"selectors": ["api"], "condition": "ready", "timeout": "10s"})
            service = waited["data"]["services"][0]
            print(f"api ready pid={service['state']['pid']}")
        elif command == "migrate":
            run = client.tool("action_run", {"action": "api/migrate"})
            action = run["data"]["action_result"]
            print(f"api/migrate#{action['run']} {action['status']} exit={action['exit_code']}")
            logs = client.tool("logs", {"selectors": ["api/migrate"], "run": action["run"]})
            for event in logs["data"]["events"]:
                if event["source"] != "kranz": print(event["text"])
            print("read again: same run, no re-execution")
            again = client.tool("action_result", {"action": "api/migrate", "run": action["run"]})
            print(f"api/migrate#{again['data']['run']} {again['data']['status']}")
        elif command == "actions":
            session = client.resource("kranz://session")
            actions = client.tool("action_list", {})
            print(f"runtime={session['session']['name']} session={session['session']['id'][:8]}")
            for action in actions["data"]:
                print(action["id"])
        elif command == "demo":
            protocol = client.initialize_result["protocolVersion"]
            print(f"MCP initialize                         -> {protocol}")
            session = client.resource("kranz://session")
            print("MCP resource kranz://session           "
                  f"-> {session['session']['name']} {session['session']['id'][:8]}")
            status = client.tool("status", {"selectors": ["api"]})["data"][0]
            print("MCP tool status {selectors: [api]}     "
                  f"-> {status['state']['status']}, ready")
            run = client.tool("action_run", {"action": "api/migrate"})
            action = run["data"]["action_result"]
            print("MCP tool action_run {api/migrate}      "
                  f"-> run #{action['run']} {action['status']}")
            again = client.tool("action_result", {"action": "api/migrate", "run": action["run"]})
            print("MCP tool action_result {same run}      "
                  f"-> run #{again['data']['run']}, no re-execution")
        else:
            raise SystemExit("usage: mcp_client.py status|logs|restart|migrate|actions|demo")
    finally:
        client.close()


if __name__ == "__main__":
    main()
