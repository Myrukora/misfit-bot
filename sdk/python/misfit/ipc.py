"""
IPC protocol handler for Misfit Python modules.

Handles JSON-based communication between Go bot and Python module via stdin/stdout.
Supports both fire-and-forget messages and request/response pattern.
"""

import json
import sys
import threading
import uuid
from typing import Any, Callable, Dict, Optional


class IPCError(Exception):
    """Raised when an IPC operation fails."""
    pass


class IPC:
    """
    IPC handler for communication with the Go bot.

    Reads JSON messages from stdin and writes JSON messages to stdout.
    Supports request/response correlation via unique message IDs.
    """

    def __init__(self):
        self._handlers: Dict[str, Callable] = {}
        self._pending: Dict[str, threading.Event] = {}
        self._responses: Dict[str, dict] = {}
        self._lock = threading.Lock()
        self._running = False
        self._read_thread: Optional[threading.Thread] = None

    def send(self, message: dict) -> None:
        """
        Send a JSON message to the Go bot via stdout.

        Args:
            message: Dictionary to send as JSON
        """
        try:
            json_str = json.dumps(message)
            sys.stdout.write(json_str + "\n")
            sys.stdout.flush()
        except Exception:
            # If we can't write to stdout, the bot is probably gone
            sys.exit(1)

    def call(self, msg_type: str, data: dict, timeout: float = 30.0) -> dict:
        """
        Send a message and wait for a correlated response.

        Args:
            msg_type: The message type (e.g., "api_request", "http_request")
            data: The message data payload
            timeout: Maximum time to wait for response in seconds

        Returns:
            The response dictionary with "data" or "error" field

        Raises:
            IPCError: If the request times out or the response contains an error
        """
        req_id = str(uuid.uuid4())
        event = threading.Event()

        with self._lock:
            self._pending[req_id] = event

        self.send({"id": req_id, "type": msg_type, **data})

        if not event.wait(timeout):
            # Leave the entry in _pending — the message loop will handle
            # any late-arriving response and clean it up, avoiding a race
            # where a response arrives between the timeout check and the pop.
            raise IPCError(f"Request {req_id} timed out after {timeout}s")

        with self._lock:
            self._pending.pop(req_id, None)
            result = self._responses.pop(req_id, None)

        if result is None:
            raise IPCError(f"Request {req_id} received no response")

        if "error" in result and result["error"]:
            raise IPCError(result["error"])

        return result

    def receive(self) -> Optional[dict]:
        """
        Read a JSON message from stdin.

        Returns:
            Parsed JSON message, or None if EOF or invalid JSON
        """
        try:
            line = sys.stdin.readline()
            if not line:
                return None  # EOF
            return json.loads(line.strip())
        except json.JSONDecodeError:
            return None
        except Exception:
            return None

    def register_handler(self, message_type: str, handler: Callable) -> None:
        """
        Register a handler for a specific message type.

        Args:
            message_type: The type of message to handle
            handler: Function to call when message is received
        """
        self._handlers[message_type] = handler

    def start(self) -> None:
        """Start the IPC message loop in a background thread."""
        self._running = True
        self._read_thread = threading.Thread(target=self._message_loop, daemon=True)
        self._read_thread.start()

    def stop(self) -> None:
        """Stop the IPC message loop."""
        self._running = False

    def _message_loop(self) -> None:
        """Main message reading loop."""
        while self._running:
            message = self.receive()
            if message is None:
                # EOF or error, exit
                break

            msg_type = message.get("type")
            msg_id = message.get("id")

            # Check if this is a response to a pending call
            if msg_id and msg_type in ("api_response", "http_response"):
                with self._lock:
                    if msg_id in self._pending:
                        self._responses[msg_id] = message
                        self._pending[msg_id].set()
                        continue

            # Otherwise dispatch to registered handler
            if msg_type in self._handlers:
                try:
                    self._handlers[msg_type](message)
                except Exception as e:
                    self.send({"type": "error", "message": str(e)})
            elif msg_type not in ("api_response", "http_response"):
                # Don't log unmatched responses as errors
                self.send({"type": "error", "message": f"Unknown message type: {msg_type}"})

    def wait_for_shutdown(self) -> None:
        """Wait for the message loop to complete."""
        if self._read_thread:
            self._read_thread.join()
