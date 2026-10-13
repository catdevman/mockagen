.PHONY: help
help:
	@mise tasks

# Explicitly use $@ to pass the target name safely
%:
	@mise run $(MAKECMDGOALS)
