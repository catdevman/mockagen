# A convenience shim only: mise is the task runner, and the tasks themselves
# live in .config/mise/tasks. `make` with no target lists them.
#
# Each goal is forwarded on its own, via $@ rather than $(MAKECMDGOALS): the
# catch-all recipe runs once per goal, so passing the whole goal list would
# invoke `mise run test lint` twice and hand the test task "lint" as an
# argument.
.PHONY: help
help:
	@mise tasks

%:
	@mise run $@
